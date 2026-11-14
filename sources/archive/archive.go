// Package archive is the Internet Archive source, searching its LibriVox
// collection: recordings of public-domain books, read by volunteers and
// free for anyone to download (https://archive.org/details/librivoxaudio).
//
// The Archive publishes a torrent for every item, so its recordings travel
// the same road as any other listing: an info hash, a magnet link, the
// downloader. Search asks the Archive's search API; Details reads the
// item's metadata for the torrent's info hash.
//
// ParseSearch and ParseItem turn API answers into candidates and need no
// network; Site adds the fetching.
package archive

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	fetch "github.com/broots144/audiobook-fetch"
)

func init() {
	fetch.RegisterSource("archive", func(cfg fetch.SourceConfig) (fetch.Source, error) {
		return New(cfg)
	})
}

const (
	// DefaultURL is the Archive's address; set ARCHIVE_URL to use another.
	DefaultURL = "https://archive.org"

	collection = "librivoxaudio"
	pageSize   = 50
)

// trackers are the Archive's own, which seed every torrent it publishes.
var trackers = []string{"http://bt1.archive.org:6969/announce", "http://bt2.archive.org:6969/announce"}

// Site searches the Internet Archive.
type Site struct {
	URL     string
	fetcher *fetch.Fetcher
}

// New returns the source, using ARCHIVE_URL if it is set.
func New(cfg fetch.SourceConfig) (*Site, error) {
	fetcher, err := fetch.NewFetcher(cfg)
	if err != nil {
		return nil, err
	}
	target := DefaultURL
	if v := os.Getenv("ARCHIVE_URL"); v != "" {
		target = v
	}
	return &Site{URL: strings.TrimRight(target, "/"), fetcher: fetcher}, nil
}

// Name implements fetch.Source.
func (s *Site) Name() string { return "Internet Archive" }

// Search implements fetch.Source. The words may be in the title or the
// author's name, so they are searched across the whole record.
func (s *Site) Search(query string, page int) ([]*fetch.Candidate, error) {
	words := strings.Fields(strings.Map(func(r rune) rune {
		if strings.ContainsRune(`+-&|!(){}[]^"~*?:\/`, r) {
			return ' ' // these mean something to the search language
		}
		return r
	}, query))
	if len(words) == 0 {
		return nil, nil
	}
	values := url.Values{
		"q":      {"collection:" + collection + " AND (" + strings.Join(words, " AND ") + ")"},
		"fl[]":   {"identifier", "title", "creator", "language", "item_size", "publicdate"},
		"sort[]": {"downloads desc"},
		"rows":   {strconv.Itoa(pageSize)},
		"page":   {strconv.Itoa(max(1, page))},
		"output": {"json"},
	}
	body, err := s.fetcher.Get(s.URL + "/advancedsearch.php?" + values.Encode())
	if err != nil {
		return nil, err
	}
	found, err := ParseSearch(body)
	for _, c := range found {
		c.URL = s.URL + "/details/" + c.URL
	}
	return found, err
}

// Details implements fetch.Source: it fills in the info hash of the item's
// torrent, which the search results do not carry.
func (s *Site) Details(c *fetch.Candidate) error {
	if c.InfoHash != "" {
		return nil
	}
	at := strings.LastIndex(c.URL, "/details/")
	if at < 0 {
		return fmt.Errorf("not an Internet Archive item: %s", c.URL)
	}
	identifier := c.URL[at+len("/details/"):]
	body, err := s.fetcher.Get(s.URL + "/metadata/" + url.PathEscape(identifier))
	if err != nil {
		return err
	}
	return ParseItem(body, c)
}

// oneOrMany reads a field the Archive writes as a string when there is one
// value and as a list when there are several.
type oneOrMany []string

func (o *oneOrMany) UnmarshalJSON(data []byte) error {
	var many []string
	if json.Unmarshal(data, &many) == nil {
		*o = many
		return nil
	}
	var one string
	if err := json.Unmarshal(data, &one); err != nil {
		*o = nil // a number or null: nothing usable
		return nil
	}
	*o = []string{one}
	return nil
}

type searchAnswer struct {
	Response struct {
		Docs []struct {
			Identifier string    `json:"identifier"`
			Title      string    `json:"title"`
			Creator    oneOrMany `json:"creator"`
			Language   oneOrMany `json:"language"`
			Size       int64     `json:"item_size"`
			Public     string    `json:"publicdate"`
		} `json:"docs"`
	} `json:"response"`
}

var rerecording = regexp.MustCompile(`(?i)\s*[\[(]?\bversion \d+\b[\])]?`)

// ParseSearch reads one answer from the search API. A candidate's URL is
// just the item's identifier until Search completes it.
func ParseSearch(body string) ([]*fetch.Candidate, error) {
	if strings.TrimSpace(body) == "" {
		return nil, nil
	}
	var data searchAnswer
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return nil, fmt.Errorf("unexpected answer from the Internet Archive: %w", err)
	}
	var out []*fetch.Candidate
	for _, doc := range data.Response.Docs {
		if doc.Identifier == "" || doc.Title == "" {
			continue
		}
		c := &fetch.Candidate{
			// LibriVox numbers its re-recordings ("Dracula (version 2)");
			// that is not a volume of the book, so it is left out.
			Heading: strings.TrimSpace(rerecording.ReplaceAllString(doc.Title, "")),
			URL:     doc.Identifier,
			Authors: doc.Creator,
			Format:  "MP3",
			Edition: "Unabridged", // LibriVox reads books whole
			Posted:  strings.SplitN(doc.Public, "T", 2)[0],
		}
		if len(doc.Creator) > 0 {
			c.Heading += " - " + strings.Join(doc.Creator, ", ")
		}
		if len(doc.Language) > 0 {
			c.Language = languageName(doc.Language[0])
		}
		if doc.Size > 0 {
			c.Size = fetch.HumanSize(doc.Size)
		}
		out = append(out, c)
	}
	return out, nil
}

// ParseItem reads an item's metadata into c: above all the info hash of
// the torrent the Archive publishes for it.
func ParseItem(body string, c *fetch.Candidate) error {
	var data struct {
		Files []struct {
			Name   string `json:"name"`
			Format string `json:"format"`
			BTIH   string `json:"btih"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return fmt.Errorf("unexpected item metadata from the Internet Archive: %w", err)
	}
	for _, file := range data.Files {
		if hash := fetch.NormalizeHash(file.BTIH); hash != "" {
			c.InfoHash = hash
			c.Trackers = trackers
			return nil
		}
	}
	return errors.New("the Internet Archive lists no torrent for this item")
}

// The Archive writes languages as three-letter codes, mostly.
var languages = map[string]string{
	"eng": "English", "en": "English", "deu": "German", "ger": "German", "fra": "French", "fre": "French",
	"spa": "Spanish", "ita": "Italian", "nld": "Dutch", "dut": "Dutch", "por": "Portuguese", "rus": "Russian",
	"jpn": "Japanese", "zho": "Chinese", "chi": "Chinese", "pol": "Polish", "swe": "Swedish", "dan": "Danish",
	"nor": "Norwegian", "fin": "Finnish", "ell": "Greek", "gre": "Greek", "lat": "Latin", "heb": "Hebrew",
	"hun": "Hungarian", "ces": "Czech", "cze": "Czech", "kor": "Korean", "ara": "Arabic", "tur": "Turkish",
	"hin": "Hindi", "mul": "Multiple",
}

func languageName(code string) string {
	if name, ok := languages[strings.ToLower(strings.TrimSpace(code))]; ok {
		return name
	}
	return strings.TrimSpace(code)
}
