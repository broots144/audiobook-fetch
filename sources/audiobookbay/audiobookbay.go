// Package audiobookbay is the AudioBook Bay source.
//
// ParseSearch and ParseDetails turn the site's pages into candidates and
// need no network; Site adds the fetching.
package audiobookbay

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"

	fetch "github.com/broots144/audiobook-fetch"
)

func init() {
	fetch.RegisterSource("audiobookbay", func(cfg fetch.SourceConfig) (fetch.Source, error) {
		return New(cfg)
	})
}

// DefaultURL is where the site lives. It moves from time to time; set
// AUDIOBOOKBAY_URL to point somewhere else.
const DefaultURL = "https://audiobookbay.lu"

// browserUserAgent is what this source has always sent, and still does.
//
// Every other source is asked under the tool's own name (fetch.UserAgent).
// This one was left alone: on 2026-11-14 two searches under the tool's own
// name got no answer at all (a timeout, not a refusal), and it could not be
// settled whether the site was down that day or does not answer clients
// that are not browsers. Until someone has seen a search succeed under
// fetch.UserAgent, changing it here risks breaking the source for nothing.
// If you do see one succeed, delete this and the line in New that uses it.
const browserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

var (
	whitespace = regexp.MustCompile(`\s+`)
	hashExact  = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)
	hashAny    = regexp.MustCompile(`\b[a-fA-F0-9]{40}\b`)
	trackerURL = regexp.MustCompile(`^(udp|https?)://`)
	languageRe = regexp.MustCompile(`Language:\s*(.+?)\s*(?:Keywords:|$)`)
	formatRe   = regexp.MustCompile(`Format:\s*(.+?)\s*/`)
	bitrateRe  = regexp.MustCompile(`Bitrate:\s*(.+?)\s*File Size:`)
	sizeRe     = regexp.MustCompile(`File Size:\s*([\d.,]+)\s*([A-Za-z]+)`)
	postedRe   = regexp.MustCompile(`Posted:\s*(\d{1,2} [A-Za-z]{3} \d{4})`)
)

// Site searches AudioBook Bay.
type Site struct {
	BaseURL string
	fetcher *fetch.Fetcher
}

// New returns the source, using AUDIOBOOKBAY_URL if it is set.
func New(cfg fetch.SourceConfig) (*Site, error) {
	fetcher, err := fetch.NewFetcher(cfg)
	if err != nil {
		return nil, err
	}
	base := DefaultURL
	if v := os.Getenv("AUDIOBOOKBAY_URL"); v != "" {
		base = v
	}
	fetcher.UserAgent = browserUserAgent
	return &Site{BaseURL: strings.TrimRight(base, "/"), fetcher: fetcher}, nil
}

// Name implements fetch.Source.
func (s *Site) Name() string { return "AudioBook Bay" }

// Search implements fetch.Source. The site matches each quoted word
// anywhere in a listing, so every word of the query is quoted.
func (s *Site) Search(query string, page int) ([]*fetch.Candidate, error) {
	words := strings.Fields(query)
	for i, w := range words {
		words[i] = `"` + w + `"`
	}
	target := fmt.Sprintf("%s/page/%d/?s=%s&cat=undefined%%2Cundefined",
		s.BaseURL, page, url.QueryEscape(strings.Join(words, " ")))
	html, err := s.fetcher.Get(target)
	if err != nil {
		return nil, err
	}
	return ParseSearch(html, s.BaseURL)
}

// Details implements fetch.Source.
func (s *Site) Details(c *fetch.Candidate) error {
	html, err := s.fetcher.Get(c.URL)
	if err != nil {
		return err
	}
	return ParseDetails(html, c)
}

func clean(text string) string {
	return strings.TrimSpace(whitespace.ReplaceAllString(strings.ReplaceAll(text, " ", " "), " "))
}

func submatch(re *regexp.Regexp, text string, group int) string {
	if m := re.FindStringSubmatch(text); m != nil {
		return m[group]
	}
	return ""
}

func resolve(baseURL, href string) string {
	base, err := url.Parse(baseURL + "/")
	if err != nil {
		return href
	}
	ref, err := url.Parse(href)
	if err != nil {
		return href
	}
	return base.ResolveReference(ref).String()
}

// ParseSearch reads the listings off a search results page.
func ParseSearch(html, baseURL string) ([]*fetch.Candidate, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, err
	}
	var out []*fetch.Candidate
	doc.Find("div.post, article.post").Each(func(_ int, post *goquery.Selection) {
		link := post.Find(".postTitle h2 a").First()
		href, ok := link.Attr("href")
		if !ok || clean(link.Text()) == "" {
			return
		}
		c := &fetch.Candidate{Heading: clean(link.Text()), URL: resolve(baseURL, href)}
		c.Language = submatch(languageRe, clean(post.Find(".postInfo").First().Text()), 1)

		content := clean(post.Find(".postContent").First().Text())
		c.Format = submatch(formatRe, content, 1)
		c.Bitrate = submatch(bitrateRe, content, 1)
		if m := sizeRe.FindStringSubmatch(content); m != nil {
			c.Size = m[1] + " " + m[2]
		}
		c.Posted = submatch(postedRe, content, 1)
		out = append(out, c)
	})
	return out, nil
}

// ParseDetails fills in authors, narrators, edition, hash and trackers from
// a listing's own page.
func ParseDetails(html string, c *fetch.Candidate) error {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return err
	}
	if h := clean(doc.Find(".postTitle h1").First().Text()); h != "" {
		c.Heading = h
	}

	texts := func(selector string) []string {
		var out []string
		doc.Find(selector).Each(func(_ int, s *goquery.Selection) {
			if t := clean(s.Text()); t != "" {
				out = append(out, t)
			}
		})
		return out
	}
	c.Authors = texts(".desc span.author")
	c.Narrators = texts(".desc span.narrator")
	c.Edition = clean(doc.Find(".desc span.is_abridged").First().Text())

	c.Trackers = nil
	seen := map[string]bool{}
	doc.Find("td").Each(func(_ int, cell *goquery.Selection) {
		next := cell.Next()
		if next.Length() == 0 || goquery.NodeName(next) != "td" {
			return
		}
		label, value := clean(cell.Text()), clean(next.Text())
		switch {
		case label == "Info Hash:" && hashExact.MatchString(value):
			c.InfoHash = strings.ToLower(value)
		case label == "Tracker:" && trackerURL.MatchString(value) && !seen[value]:
			seen[value] = true
			c.Trackers = append(c.Trackers, value)
		}
	})

	if c.InfoHash == "" {
		c.InfoHash = strings.ToLower(hashAny.FindString(doc.Find("#magnetLink").Text()))
	}
	return nil
}
