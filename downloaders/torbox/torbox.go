// Package torbox is the TorBox downloader (https://torbox.app).
//
// It makes only the API calls needed to turn magnet links into files.
// Request and response shapes follow the official SDK
// (TorBox-App/torbox-sdk-go) and were checked against the live API.
package torbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	fetch "github.com/broots144/audiobook-fetch"
)

func init() {
	fetch.RegisterDownloader("torbox", func(apiKey string) (fetch.Downloader, error) {
		if strings.TrimSpace(apiKey) == "" {
			return nil, errors.New("TorBox needs an API key")
		}
		return New(apiKey), nil
	})
}

// BaseURL is the v1 API root.
const BaseURL = "https://api.torbox.app/v1/api"

const listPageSize = 1000

// Client talks to the TorBox API with one account's key.
type Client struct {
	BaseURL      string
	Key          string
	HTTP         *http.Client
	Retries      int           // extra attempts after a 408, 429, 5xx or network error
	MaxRetryWait time.Duration // cap on how long one Retry-After may make us wait
	Sleep        func(time.Duration)
}

// New returns a client with sensible defaults.
func New(key string) *Client {
	return &Client{
		BaseURL:      BaseURL,
		Key:          strings.TrimSpace(key),
		HTTP:         &http.Client{Timeout: 60 * time.Second},
		Retries:      4,
		MaxRetryWait: 5 * time.Minute,
		Sleep:        time.Sleep,
	}
}

// Name implements fetch.Downloader.
func (c *Client) Name() string { return "TorBox" }

// Every response is wrapped the same way.
type envelope struct {
	Success bool            `json:"success"`
	Error   any             `json:"error"`
	Detail  string          `json:"detail"`
	Data    json.RawMessage `json:"data"`
}

func retryable(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

// call makes one API request, retrying the failures that are worth retrying
// and honouring Retry-After. body may be nil.
func (c *Client) call(method, path string, query url.Values, body []byte, contentType string, out any) error {
	target := strings.TrimRight(c.BaseURL, "/") + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var lastErr error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		wait, retry, err := c.callOnce(method, target, body, contentType, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry || attempt == c.Retries {
			break
		}
		if wait <= 0 {
			wait = 2 * time.Second << attempt
		}
		c.Sleep(min(wait, c.MaxRetryWait))
	}
	return fmt.Errorf("torbox %s %s: %w", method, path, lastErr)
}

func (c *Client) callOnce(method, target string, body []byte, contentType string, out any) (wait time.Duration, retry bool, err error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, target, reader)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("User-Agent", fetch.AppName)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// The key travels in one request's URL; keep it out of error text.
		return 0, true, errors.New(strings.ReplaceAll(err.Error(), c.Key, "<key>"))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return 0, true, err
	}

	var env envelope
	parsed := json.Unmarshal(raw, &env) == nil
	if resp.StatusCode != http.StatusOK || !parsed || !env.Success {
		if secs, convErr := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64); convErr == nil && secs > 0 {
			wait = time.Duration(secs * float64(time.Second))
		}
		reason := env.Detail
		if reason == "" {
			reason = strings.TrimSpace(string(raw[:min(len(raw), 200)]))
		}
		if code, ok := env.Error.(string); ok && code != "" {
			reason = code + ": " + reason
		}
		return wait, retryable(resp.StatusCode), fmt.Errorf("HTTP %d: %s", resp.StatusCode, reason)
	}
	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return 0, false, fmt.Errorf("unexpected response shape: %w", err)
		}
	}
	return 0, false, nil
}

// torrent and torrentFile are TorBox's own shapes for an item and its files.
type torrent struct {
	ID               int64         `json:"id"`
	Hash             string        `json:"hash"`
	Name             string        `json:"name"`
	DownloadState    string        `json:"download_state"`
	DownloadFinished bool          `json:"download_finished"`
	DownloadPresent  bool          `json:"download_present"`
	Progress         float64       `json:"progress"`
	Files            []torrentFile `json:"files"`
}

type torrentFile struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"` // path relative to the item, folders included
	ShortName string `json:"short_name"`
	Size      int64  `json:"size"`
}

func (t torrent) item() fetch.Item {
	item := fetch.Item{
		ID:       strconv.FormatInt(t.ID, 10),
		Hash:     strings.ToLower(t.Hash),
		Name:     t.Name,
		Ready:    t.DownloadFinished && t.DownloadPresent,
		State:    t.DownloadState,
		Progress: t.Progress,
	}
	for _, f := range t.Files {
		path := f.Name
		if path == "" {
			path = f.ShortName
		}
		item.Files = append(item.Files, fetch.File{ID: strconv.FormatInt(f.ID, 10), Path: path, Size: f.Size})
	}
	return item
}

// List implements fetch.Downloader.
func (c *Client) List() ([]fetch.Item, error) {
	var all []fetch.Item
	for offset := 0; ; offset += listPageSize {
		var page []torrent
		query := url.Values{"bypass_cache": {"true"}, "limit": {strconv.Itoa(listPageSize)}, "offset": {strconv.Itoa(offset)}}
		if err := c.call(http.MethodGet, "/torrents/mylist", query, nil, "", &page); err != nil {
			return nil, err
		}
		for _, t := range page {
			all = append(all, t.item())
		}
		if len(page) < listPageSize {
			return all, nil
		}
	}
}

// CheckCached implements fetch.Downloader.
func (c *Client) CheckCached(hashes []string) (map[string]bool, error) {
	cached := map[string]bool{}
	for start := 0; start < len(hashes); start += 100 {
		chunk := hashes[start:min(start+100, len(hashes))]
		var found map[string]json.RawMessage
		query := url.Values{"hash": {strings.Join(chunk, ",")}, "format": {"object"}, "list_files": {"false"}}
		if err := c.call(http.MethodGet, "/torrents/checkcached", query, nil, "", &found); err != nil {
			return nil, err
		}
		for hash := range found {
			cached[strings.ToLower(hash)] = true
		}
	}
	return cached, nil
}

// Add implements fetch.Downloader. TorBox starts fetching the magnet, or has
// it at once if it was cached. Items beyond the account's active limit are
// queued and appear in List once TorBox starts on them.
func (c *Client) Add(magnet string) error {
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	_ = writer.WriteField("magnet", magnet)
	_ = writer.Close()
	return c.call(http.MethodPost, "/torrents/createtorrent", nil, form.Bytes(), writer.FormDataContentType(), nil)
}

// Link implements fetch.Downloader.
func (c *Client) Link(item fetch.Item, file fetch.File) (string, error) {
	var link string
	query := url.Values{"token": {c.Key}, "torrent_id": {item.ID}, "file_id": {file.ID}}
	if err := c.call(http.MethodGet, "/torrents/requestdl", query, nil, "", &link); err != nil {
		return "", err
	}
	if link == "" {
		return "", fmt.Errorf("torbox returned no link for item %s file %s", item.ID, file.ID)
	}
	return link, nil
}

// Remove implements fetch.Downloader.
func (c *Client) Remove(item fetch.Item) error {
	id, err := strconv.ParseInt(item.ID, 10, 64)
	if err != nil {
		return fmt.Errorf("not a TorBox item id: %q", item.ID)
	}
	body, _ := json.Marshal(map[string]any{"torrent_id": id, "operation": "delete"})
	return c.call(http.MethodPost, "/torrents/controltorrent", nil, body, "application/json", nil)
}
