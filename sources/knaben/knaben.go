// Package knaben is the Knaben source: a search engine over many public
// trackers (The Pirate Bay, Nyaa and others) with a plain JSON API.
//
// Unlike a site with one page per listing, its search results are already
// complete: every hit carries its info hash, its size and how many people
// are seeding it. So Details has nothing to do, and listings nobody is
// seeding can be passed over before any time is spent on them.
//
// Parse turns an API answer into candidates and needs no network; Site adds
// the fetching.
package knaben

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	fetch "github.com/broots144/audiobook-fetch"
)

func init() {
	fetch.RegisterSource("knaben", func(cfg fetch.SourceConfig) (fetch.Source, error) {
		return New(cfg)
	})
}

const (
	// DefaultURL is the API endpoint; set KNABEN_API_URL to use another.
	DefaultURL = "https://api.knaben.org/v1"

	// categoryAudiobook keeps music albums and films that share a book's
	// title out of the results.
	categoryAudiobook = 1003000
	pageSize          = 50
)

// Site searches Knaben.
type Site struct {
	URL     string
	fetcher *fetch.Fetcher
}

// New returns the source, using KNABEN_API_URL if it is set.
func New(cfg fetch.SourceConfig) (*Site, error) {
	fetcher, err := fetch.NewFetcher(cfg)
	if err != nil {
		return nil, err
	}
	target := DefaultURL
	if v := os.Getenv("KNABEN_API_URL"); v != "" {
		target = v
	}
	return &Site{URL: target, fetcher: fetcher}, nil
}

// Name implements fetch.Source.
func (s *Site) Name() string { return "Knaben" }

// request is one search as the API wants it.
type request struct {
	SearchType     string `json:"search_type"` // "100%": every word must be in the title
	SearchField    string `json:"search_field"`
	Query          string `json:"query"`
	Categories     []int  `json:"categories"`
	OrderBy        string `json:"order_by"`
	OrderDirection string `json:"order_direction"`
	From           int    `json:"from"`
	Size           int    `json:"size"`
	HideUnsafe     bool   `json:"hide_unsafe"`
	HideXXX        bool   `json:"hide_xxx"`
}

// Search implements fetch.Source.
func (s *Site) Search(query string, page int) ([]*fetch.Candidate, error) {
	body, err := s.fetcher.PostJSON(s.URL, request{
		SearchType: "100%", SearchField: "title", Query: query,
		Categories: []int{categoryAudiobook},
		OrderBy:    "seeders", OrderDirection: "desc",
		From: (max(1, page) - 1) * pageSize, Size: pageSize,
		HideUnsafe: true, HideXXX: true,
	})
	if err != nil {
		return nil, err
	}
	return Parse(body)
}

// Details implements fetch.Source. Search results are already complete.
func (s *Site) Details(*fetch.Candidate) error { return nil }

type answer struct {
	Hits []struct {
		Title   string `json:"title"`
		Hash    string `json:"hash"`
		Magnet  string `json:"magnetUrl"`
		Details string `json:"details"`
		Tracker string `json:"tracker"`
		Date    string `json:"date"`
		Bytes   int64  `json:"bytes"`
		Seeders int    `json:"seeders"`
	} `json:"hits"`
}

// Parse reads one API answer. Hits without an info hash are dropped: there
// would be nothing to download. The same files listed by several trackers
// are kept once, with the highest seeder count seen.
func Parse(body string) ([]*fetch.Candidate, error) {
	if strings.TrimSpace(body) == "" {
		return nil, nil
	}
	var data answer
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return nil, fmt.Errorf("unexpected answer from Knaben: %w", err)
	}
	var out []*fetch.Candidate
	byHash := map[string]*fetch.Candidate{}
	for _, hit := range data.Hits {
		hash := fetch.NormalizeHash(hit.Hash)
		if hash == "" {
			continue
		}
		seeders := hit.Seeders
		if known := byHash[hash]; known != nil {
			if seeders > *known.Seeders {
				known.Seeders = &seeders
			}
			continue
		}
		c := &fetch.Candidate{
			URL:      hit.Details,
			InfoHash: hash,
			Size:     fetch.HumanSize(hit.Bytes),
			Posted:   strings.SplitN(hit.Date, "T", 2)[0],
			Trackers: trackers(hit.Magnet),
			Seeders:  &seeders,
		}
		if c.URL == "" {
			c.URL = "knaben:" + hash
		}
		fetch.ParseRelease(hit.Title, c)
		byHash[hash] = c
		out = append(out, c)
	}
	return out, nil
}

// trackers pulls the announce addresses out of a magnet link.
func trackers(magnet string) []string {
	_, query, found := strings.Cut(magnet, "?")
	if !found {
		return nil
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return nil
	}
	return values["tr"]
}
