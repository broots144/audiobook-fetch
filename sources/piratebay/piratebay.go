// Package piratebay is The Pirate Bay source, through the JSON API its own
// web pages are built on.
//
// As with any general tracker, a listing is a release name plus numbers, so
// search results are already complete (info hash, size, seeders) and Details
// has nothing to do.
//
// Parse turns an API answer into candidates and needs no network; Site adds
// the fetching.
package piratebay

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	fetch "github.com/broots144/audiobook-fetch"
)

func init() {
	fetch.RegisterSource("piratebay", func(cfg fetch.SourceConfig) (fetch.Source, error) {
		return New(cfg)
	})
}

const (
	// DefaultURL is the API's address; set PIRATEBAY_API_URL to use another.
	DefaultURL = "https://apibay.org"
	// DescriptionURL is where a listing's own page lives, for the report.
	DescriptionURL = "https://thepiratebay.org/description.php?id="

	categoryAudiobook = "102"
)

// Site searches The Pirate Bay.
type Site struct {
	BaseURL string
	fetcher *fetch.Fetcher
}

// New returns the source, using PIRATEBAY_API_URL if it is set.
func New(cfg fetch.SourceConfig) (*Site, error) {
	fetcher, err := fetch.NewFetcher(cfg)
	if err != nil {
		return nil, err
	}
	base := DefaultURL
	if v := os.Getenv("PIRATEBAY_API_URL"); v != "" {
		base = v
	}
	return &Site{BaseURL: strings.TrimRight(base, "/"), fetcher: fetcher}, nil
}

// Name implements fetch.Source.
func (s *Site) Name() string { return "The Pirate Bay" }

// Search implements fetch.Source. The API answers with everything it has in
// one go, so there is only ever one page.
func (s *Site) Search(query string, page int) ([]*fetch.Candidate, error) {
	if page > 1 {
		return nil, nil
	}
	// Ask for audiobooks only. That filter sometimes comes back empty when
	// there are audiobooks to be had, so ask again without it and do the
	// filtering here.
	for _, category := range []string{"&cat=" + categoryAudiobook, ""} {
		body, err := s.fetcher.Get(s.BaseURL + "/q.php?q=" + url.QueryEscape(query) + category)
		if err != nil {
			return nil, err
		}
		found, err := Parse(body)
		if err != nil || len(found) > 0 {
			return found, err
		}
	}
	return nil, nil
}

// Details implements fetch.Source. Search results are already complete.
func (s *Site) Details(*fetch.Candidate) error { return nil }

// The API sends every field as a string.
type row struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	InfoHash string `json:"info_hash"`
	Seeders  string `json:"seeders"`
	Size     string `json:"size"`
	Added    string `json:"added"`
	Category string `json:"category"`
}

// Parse reads one API answer, keeping the audiobooks. "Nothing found" comes
// as a single placeholder row with id 0, which is dropped like any other row
// without a usable hash.
func Parse(body string) ([]*fetch.Candidate, error) {
	if strings.TrimSpace(body) == "" {
		return nil, nil
	}
	var rows []row
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		return nil, fmt.Errorf("unexpected answer from The Pirate Bay: %w", err)
	}
	var out []*fetch.Candidate
	for _, r := range rows {
		hash := fetch.NormalizeHash(r.InfoHash)
		if r.ID == "0" || r.Category != categoryAudiobook || hash == "" || strings.Trim(hash, "0") == "" {
			continue
		}
		c := &fetch.Candidate{URL: DescriptionURL + r.ID, InfoHash: hash}
		if n, err := strconv.Atoi(r.Seeders); err == nil {
			c.Seeders = &n
		}
		if n, err := strconv.ParseInt(r.Size, 10, 64); err == nil {
			c.Size = fetch.HumanSize(n)
		}
		if n, err := strconv.ParseInt(r.Added, 10, 64); err == nil && n > 0 {
			c.Posted = time.Unix(n, 0).UTC().Format("2006-01-02")
		}
		fetch.ParseRelease(r.Name, c)
		out = append(out, c)
	}
	return out, nil
}
