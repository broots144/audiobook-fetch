// Package archive is the Internet Archive downloader: it fetches an item's
// files straight from archive.org over HTTPS.
//
// The Archive's torrents lean on its own web servers rather than on people
// seeding, so a torrent service makes no headway with them. But the same
// files are served directly, to anyone, with no account. This downloader
// takes one of the Archive's magnet links (as the archive source produces),
// finds the item it belongs to by its info hash, and serves the item's
// original audio files, leaving out the re-encodings and by-products that
// the Archive keeps beside them.
//
// There is no account and so nothing to list or remove: what it "holds" is
// simply what it has been given during this run.
package archive

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"

	fetch "github.com/broots144/audiobook-fetch"
)

func init() {
	fetch.RegisterDownloader("archive", func(string) (fetch.Downloader, error) { return New() })
}

// DefaultURL is the Archive's address; set ARCHIVE_URL to use another.
const DefaultURL = "https://archive.org"

// Handles reports whether a magnet link is one of the Archive's, and so one
// this downloader can fetch and a torrent service cannot.
func Handles(magnet string) bool {
	unescaped, err := url.QueryUnescape(magnet)
	return err == nil && strings.Contains(unescaped, ".archive.org:")
}

// Client fetches from the Internet Archive.
type Client struct {
	URL     string
	fetcher *fetch.Fetcher

	mu    sync.Mutex
	items map[string]fetch.Item // by info hash
}

// New returns the downloader, using ARCHIVE_URL if it is set.
func New() (*Client, error) {
	fetcher, err := fetch.NewFetcher(fetch.SourceConfig{})
	if err != nil {
		return nil, err
	}
	target := DefaultURL
	if v := os.Getenv("ARCHIVE_URL"); v != "" {
		target = v
	}
	return &Client{URL: strings.TrimRight(target, "/"), fetcher: fetcher, items: map[string]fetch.Item{}}, nil
}

// Name implements fetch.Downloader.
func (c *Client) Name() string { return "Internet Archive" }

// List implements fetch.Downloader.
func (c *Client) List() ([]fetch.Item, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	items := make([]fetch.Item, 0, len(c.items))
	for _, item := range c.items {
		items = append(items, item)
	}
	return items, nil
}

// CheckCached implements fetch.Downloader. Everything the Archive has can
// be served at once.
func (c *Client) CheckCached(hashes []string) (map[string]bool, error) {
	cached := make(map[string]bool, len(hashes))
	for _, hash := range hashes {
		cached[strings.ToLower(hash)] = true
	}
	return cached, nil
}

// Add implements fetch.Downloader: it finds the item the link belongs to
// and works out which of its files to fetch.
func (c *Client) Add(magnet string) error {
	links, _ := fetch.ParseMagnets(magnet)
	if len(links) != 1 {
		return errors.New("not a magnet link")
	}
	hash := links[0].Hash
	identifier, err := c.identifier(hash)
	if err != nil {
		return err
	}
	body, err := c.fetcher.Get(c.URL + "/metadata/" + url.PathEscape(identifier))
	if err != nil {
		return err
	}
	item, err := ParseItem(body)
	if err != nil {
		return err
	}
	item.ID, item.Hash = identifier, hash
	c.mu.Lock()
	c.items[hash] = item
	c.mu.Unlock()
	return nil
}

// identifier finds the Archive item whose torrent has this info hash.
func (c *Client) identifier(hash string) (string, error) {
	values := url.Values{"q": {"btih:" + hash}, "fl[]": {"identifier"}, "rows": {"1"}, "output": {"json"}}
	body, err := c.fetcher.Get(c.URL + "/advancedsearch.php?" + values.Encode())
	if err != nil {
		return "", err
	}
	var data struct {
		Response struct {
			Docs []struct {
				Identifier string `json:"identifier"`
			} `json:"docs"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return "", fmt.Errorf("unexpected answer from the Internet Archive: %w", err)
	}
	if len(data.Response.Docs) == 0 || data.Response.Docs[0].Identifier == "" {
		return "", fmt.Errorf("the Internet Archive has no item with the info hash %s", hash)
	}
	return data.Response.Docs[0].Identifier, nil
}

// Link implements fetch.Downloader.
func (c *Client) Link(item fetch.Item, file fetch.File) (string, error) {
	return c.URL + "/download/" + url.PathEscape(item.ID) + "/" + escapePath(file.ID), nil
}

func escapePath(name string) string {
	parts := strings.Split(name, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// Remove implements fetch.Downloader. There is no account to remove from.
func (c *Client) Remove(item fetch.Item) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, strings.ToLower(item.Hash))
	return nil
}

var audio = map[string]bool{".mp3": true, ".m4b": true, ".m4a": true, ".flac": true, ".ogg": true, ".opus": true, ".wav": true}

// ParseItem reads an item's metadata into the files worth fetching: the
// audio as it was uploaded, and the cover. The Archive also keeps each
// track re-encoded twice over, with spectrograms and analysis files; those
// are left where they are. An item whose originals are not audio (uploaded
// as FLAC and listed oddly, say) falls back to the audio it does list.
func ParseItem(body string) (fetch.Item, error) {
	var data struct {
		Metadata struct {
			Title   string `json:"title"`
			Creator any    `json:"creator"`
		} `json:"metadata"`
		Files []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
			Format string `json:"format"`
			Size   string `json:"size"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return fetch.Item{}, fmt.Errorf("unexpected item metadata from the Internet Archive: %w", err)
	}
	item := fetch.Item{Name: strings.TrimSpace(data.Metadata.Title), Ready: true, State: "available", Progress: 1}
	switch creator := data.Metadata.Creator.(type) {
	case string:
		item.Name += " - " + creator
	case []any:
		if len(creator) > 0 {
			item.Name += " - " + fmt.Sprint(creator[0])
		}
	}
	var originals, others, covers []fetch.File
	for _, f := range data.Files {
		size, err := strconv.ParseInt(f.Size, 10, 64)
		if err != nil || size <= 0 || f.Name == "" {
			continue
		}
		file := fetch.File{ID: f.Name, Path: f.Name, Size: size}
		ext := strings.ToLower(path.Ext(f.Name))
		switch {
		case audio[ext] && f.Source == "original":
			originals = append(originals, file)
		case audio[ext]:
			others = append(others, file)
		case f.Source == "original" && (ext == ".jpg" || ext == ".jpeg" || ext == ".png") && f.Format != "Item Tile":
			covers = append(covers, file)
		}
	}
	item.Files = originals
	if len(item.Files) == 0 {
		item.Files = others
	}
	if len(item.Files) == 0 {
		return fetch.Item{}, errors.New("the Internet Archive lists no audio files for this item")
	}
	item.Files = append(item.Files, covers...)
	return item, nil
}
