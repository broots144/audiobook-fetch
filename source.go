package magnetbatch

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// Talking to the site: search pages, details pages, magnet building.
// The Parse* functions are pure (HTML in, data out); Client adds the network,
// pacing and retries.

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// FallbackTrackers are used only when a details page lists none of its own.
var FallbackTrackers = []string{
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://open.stealth.si:80/announce",
	"udp://exodus.desync.com:6969/announce",
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://tracker.tiny-vps.com:6969/announce",
	"udp://tracker.dler.org:6969/announce",
	"http://tracker2.dler.org:80/announce",
	"http://tracker.bt4g.com:2095/announce",
}

var (
	whitespace  = regexp.MustCompile(`\s+`)
	hashExact   = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)
	hashAny     = regexp.MustCompile(`\b[a-fA-F0-9]{40}\b`)
	trackerURL  = regexp.MustCompile(`^(udp|https?)://`)
	languageRe  = regexp.MustCompile(`Language:\s*(.+?)\s*(?:Keywords:|$)`)
	formatRe    = regexp.MustCompile(`Format:\s*(.+?)\s*/`)
	bitrateRe   = regexp.MustCompile(`Bitrate:\s*(.+?)\s*File Size:`)
	sizeRe      = regexp.MustCompile(`File Size:\s*([\d.,]+)\s*([A-Za-z]+)`)
	postedRe    = regexp.MustCompile(`Posted:\s*(\d{1,2} [A-Za-z]{3} \d{4})`)
	querySplit  = regexp.MustCompile(`[\s\-–—/]+`)
	nonWordChar = regexp.MustCompile(`[^\pL\pN]`)
)

// DefaultBaseURL can be overridden with MAGNET_BATCH_BASE_URL.
func DefaultBaseURL() string {
	if v := os.Getenv("MAGNET_BATCH_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://audiobookbay.lu"
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
func ParseSearch(html, baseURL string) ([]*Candidate, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, err
	}
	var out []*Candidate
	doc.Find("div.post, article.post").Each(func(_ int, post *goquery.Selection) {
		link := post.Find(".postTitle h2 a").First()
		href, ok := link.Attr("href")
		if !ok || clean(link.Text()) == "" {
			return
		}
		c := &Candidate{Heading: clean(link.Text()), URL: resolve(baseURL, href)}
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

// ParseDetails fills in authors, narrators, edition, hash and trackers.
func ParseDetails(html string, c *Candidate) error {
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

func escape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// BuildMagnet assembles a magnet link. With no trackers, FallbackTrackers are used.
func BuildMagnet(infoHash, name string, trackers []string) string {
	if len(trackers) == 0 {
		trackers = FallbackTrackers
	}
	var b strings.Builder
	b.WriteString("magnet:?xt=urn:btih:" + infoHash + "&dn=" + escape(name))
	for _, t := range trackers {
		b.WriteString("&tr=" + escape(t))
	}
	return b.String()
}

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

// BuildQueries returns searches to try, narrowest first: title plus author
// surname, then the title alone.
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

// Client fetches pages politely: one request at a time, a pause between
// requests, and backoff when the site pushes back.
type Client struct {
	BaseURL  string
	Delay    time.Duration
	Retries  int
	HTTP     *http.Client
	Requests int // how many requests have been made

	last time.Time
}

// NewClient returns a Client with sensible defaults.
func NewClient() *Client {
	return &Client{
		BaseURL: DefaultBaseURL(),
		Delay:   3 * time.Second,
		Retries: 2,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) fetchOnce(target string) (body string, retry bool, err error) {
	if wait := c.Delay - time.Since(c.last); wait > 0 {
		time.Sleep(wait)
	}
	c.last = time.Now()
	c.Requests++

	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	resp, err := c.HTTP.Do(req)
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

func (c *Client) get(target string) (string, error) {
	var lastErr error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		body, retry, err := c.fetchOnce(target)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retry {
			break
		}
		if attempt < c.Retries {
			time.Sleep(min(60*time.Second, 5*time.Second<<attempt))
		}
	}
	return "", fmt.Errorf("%s: %w", target, lastErr)
}

// Search runs one search and returns the listings on the given page (1-based).
func (c *Client) Search(query string, page int) ([]*Candidate, error) {
	words := strings.Fields(query)
	for i, w := range words {
		words[i] = `"` + w + `"`
	}
	target := fmt.Sprintf("%s/page/%d/?s=%s&cat=undefined%%2Cundefined",
		c.BaseURL, page, url.QueryEscape(strings.Join(words, " ")))
	html, err := c.get(target)
	if err != nil {
		return nil, err
	}
	return ParseSearch(html, c.BaseURL)
}

// Details loads a candidate's own page and fills in the rest of its fields.
func (c *Client) Details(cand *Candidate) error {
	html, err := c.get(cand.URL)
	if err != nil {
		return err
	}
	return ParseDetails(html, cand)
}
