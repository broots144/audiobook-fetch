package audiobookfetch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Sources: where listings come from.

// Source is a place to search for audiobook listings.
type Source interface {
	// Name is how the source is shown to people, e.g. "AudioBook Bay".
	Name() string
	// Search returns the listings on one results page (pages start at 1),
	// filled in as far as the results page allows. No results is not an error.
	Search(query string, page int) ([]*Candidate, error)
	// Details completes a candidate from its own page: authors, narrators,
	// edition, trackers and, above all, the info hash. A source whose
	// search results are already complete does nothing here.
	Details(c *Candidate) error
}

// SourceConfig is what every source is given when it is created.
type SourceConfig struct {
	Delay time.Duration // pause between requests
	Proxy string        // optional proxy for the source's requests
}

// SourceFactory creates a source.
type SourceFactory func(SourceConfig) (Source, error)

var sourceFactories = map[string]SourceFactory{}

// RegisterSource makes a source available under name. Call it from init.
func RegisterSource(name string, factory SourceFactory) { sourceFactories[name] = factory }

// NewSource creates the source registered under name.
func NewSource(name string, cfg SourceConfig) (Source, error) {
	factory, ok := sourceFactories[name]
	if !ok {
		return nil, fmt.Errorf("unknown source %q (available: %s)", name, strings.Join(SourceNames(), ", "))
	}
	return factory(cfg)
}

// SourceNames lists the registered sources.
func SourceNames() []string { return sortedKeys(sourceFactories) }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

const fetcherUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// Fetcher gets web pages politely for a source: one request at a time, a
// pause between requests, and backoff when the site pushes back.
type Fetcher struct {
	Delay   time.Duration
	Retries int
	HTTP    *http.Client

	last time.Time
}

// NewFetcher returns a Fetcher set up from a source's config.
func NewFetcher(cfg SourceConfig) (*Fetcher, error) {
	f := &Fetcher{Delay: cfg.Delay, Retries: 2, HTTP: &http.Client{Timeout: 30 * time.Second}}
	if cfg.Proxy != "" {
		proxy, err := url.Parse(cfg.Proxy)
		if err != nil || proxy.Host == "" {
			return nil, fmt.Errorf("not a usable proxy address: %q", cfg.Proxy)
		}
		f.HTTP.Transport = &http.Transport{Proxy: http.ProxyURL(proxy)}
	}
	return f, nil
}

// Get returns the body of a page. A page that does not exist is "" with no error.
func (f *Fetcher) Get(target string) (string, error) { return f.do(target, nil) }

// PostJSON sends payload as JSON and returns the answer's body, for sources
// that are searched through an API rather than a web page.
func (f *Fetcher) PostJSON(target string, payload any) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return f.do(target, data)
}

func (f *Fetcher) do(target string, payload []byte) (string, error) {
	var lastErr error
	for attempt := 0; attempt <= f.Retries; attempt++ {
		body, retry, err := f.once(target, payload)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retry {
			break
		}
		if attempt < f.Retries {
			time.Sleep(min(60*time.Second, 5*time.Second<<attempt))
		}
	}
	return "", fmt.Errorf("%s: %w", target, lastErr)
}

func (f *Fetcher) once(target string, payload []byte) (body string, retry bool, err error) {
	if wait := f.Delay - time.Since(f.last); wait > 0 {
		time.Sleep(wait)
	}
	f.last = time.Now()

	method, content := http.MethodGet, io.Reader(nil)
	if payload != nil {
		method, content = http.MethodPost, bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, target, content)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("User-Agent", fetcherUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return "", true, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", false, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return "", true, fmt.Errorf("HTTP %d", resp.StatusCode)
	case resp.StatusCode >= 400:
		return "", false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", true, err
	}
	return string(data), false, nil
}

var (
	querySplit  = regexp.MustCompile(`[\s\-–—/]+`)
	nonWordChar = regexp.MustCompile(`[^\pL\pN]`)
)

func queryWords(text string) []string {
	var out []string
	for _, raw := range querySplit.Split(text, -1) {
		// "Ender's" may be listed with a straight or a curly apostrophe, or
		// none. The part before it matches all three.
		raw = apostrophes.Split(raw, 2)[0]
		word := nonWordChar.ReplaceAllString(raw, "")
		if len([]rune(word)) > 1 && !articles[strings.ToLower(word)] {
			out = append(out, word)
		}
	}
	return out
}

// BuildQueries returns the searches to try for a book, narrowest first:
// title plus author surname, then the title alone. The words are plain, so
// each source can quote or join them however its search wants.
func BuildQueries(w Wanted) []string {
	titleWords := queryWords(MainTitle(w.Title))
	if len(titleWords) == 0 {
		titleWords = queryWords(w.Title)
	}
	var queries []string
	if authors := SplitAuthors(w.Author); len(authors) > 0 {
		if name := queryWords(authors[0]); len(name) > 0 {
			queries = append(queries, strings.Join(append(append([]string{}, titleWords...), name[len(name)-1]), " "))
		}
	}
	if title := strings.Join(titleWords, " "); title != "" && (len(queries) == 0 || queries[0] != title) {
		queries = append(queries, title)
	}
	return queries
}
