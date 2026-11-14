package torbox

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	fetch "github.com/broots144/audiobook-fetch"
)

const (
	key   = "secret-key-123"
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fakeAPI answers the TorBox endpoints the client uses, and serves one file.
type fakeAPI struct {
	mu         sync.Mutex
	srv        *httptest.Server
	torrents   []map[string]any
	cached     map[string]bool
	rateLimits int // answer this many API calls with 429 first
	calls      int
	addedForms []string
	deleted    []map[string]any
	linkQuery  string
}

func newFakeAPI(t *testing.T) (*fakeAPI, *Client) {
	f := &fakeAPI{cached: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	c := New(key)
	c.BaseURL = f.srv.URL
	c.Sleep = func(time.Duration) {}
	return f, c
}

func (f *fakeAPI) reply(w http.ResponseWriter, status int, success bool, detail string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"success": success, "error": nil, "detail": detail, "data": data})
}

func (f *fakeAPI) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/cdn/file" {
		_, _ = io.WriteString(w, "file body")
		return
	}
	f.calls++
	if r.Header.Get("Authorization") != "Bearer "+key {
		f.reply(w, http.StatusForbidden, false, "bad key", nil)
		return
	}
	if f.rateLimits > 0 {
		f.rateLimits--
		w.Header().Set("Retry-After", "7")
		f.reply(w, http.StatusTooManyRequests, false, "slow down", nil)
		return
	}
	switch r.URL.Path {
	case "/torrents/mylist":
		if r.URL.Query().Get("bypass_cache") != "true" {
			f.reply(w, http.StatusBadRequest, false, "expected a fresh listing", nil)
			return
		}
		f.reply(w, http.StatusOK, true, "ok", f.torrents)
	case "/torrents/checkcached":
		found := map[string]any{}
		for _, hash := range strings.Split(r.URL.Query().Get("hash"), ",") {
			if f.cached[hash] {
				found[strings.ToUpper(hash)] = map[string]any{"hash": hash, "size": 1}
			}
		}
		f.reply(w, http.StatusOK, true, "ok", found)
	case "/torrents/createtorrent":
		magnet := r.FormValue("magnet")
		f.addedForms = append(f.addedForms, magnet)
		hash := magnet[strings.Index(magnet, "btih:")+5:][:40]
		f.torrents = append(f.torrents, map[string]any{"id": 900 + len(f.torrents), "hash": hash, "name": "added",
			"download_state": "cached", "download_finished": true, "download_present": true,
			"files": []map[string]any{{"id": 1, "name": "Added/file.bin", "short_name": "file.bin", "size": 9}}})
		f.reply(w, http.StatusOK, true, "added", map[string]any{"torrent_id": 900, "hash": hash})
	case "/torrents/requestdl":
		f.linkQuery = r.URL.RawQuery
		f.reply(w, http.StatusOK, true, "ok", f.srv.URL+"/cdn/file")
	case "/torrents/controltorrent":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.deleted = append(f.deleted, body)
		kept := f.torrents[:0]
		for _, t := range f.torrents {
			if fmt.Sprint(t["id"]) != fmt.Sprint(body["torrent_id"]) {
				kept = append(kept, t)
			}
		}
		f.torrents = kept
		f.reply(w, http.StatusOK, true, "deleted", nil)
	default:
		f.reply(w, http.StatusNotFound, false, "no such endpoint", nil)
	}
}

func TestListTranslatesTorBoxItems(t *testing.T) {
	f, c := newFakeAPI(t)
	f.torrents = []map[string]any{
		{"id": 41, "hash": strings.ToUpper(hashA), "name": "Ready One", "download_state": "cached",
			"download_finished": true, "download_present": true, "progress": 1,
			"files": []map[string]any{
				{"id": 0, "name": "Ready One/a.mp3", "short_name": "a.mp3", "size": 10},
				{"id": 1, "name": "", "short_name": "b.mp3", "size": 20}}},
		{"id": 42, "hash": hashB, "name": "Still Coming", "download_state": "downloading",
			"download_finished": false, "download_present": false, "progress": 0.4, "files": []any{}},
		{"id": 43, "hash": "cc", "name": "Finished But Gone", "download_finished": true, "download_present": false},
	}
	items, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items", len(items))
	}
	ready := items[0]
	if ready.ID != "41" || ready.Hash != hashA || !ready.Ready || ready.State != "cached" || len(ready.Files) != 2 {
		t.Errorf("ready item: %+v", ready)
	}
	if ready.Files[0] != (fetch.File{ID: "0", Path: "Ready One/a.mp3", Size: 10}) || ready.Files[1].Path != "b.mp3" {
		t.Errorf("files: %+v", ready.Files)
	}
	if items[1].Ready || items[1].Progress != 0.4 || items[2].Ready {
		t.Errorf("unfinished or absent items must not be ready: %+v, %+v", items[1], items[2])
	}
}

func TestCallsSendWhatTorBoxExpects(t *testing.T) {
	f, c := newFakeAPI(t)
	f.cached[hashA] = true

	cached, err := c.CheckCached([]string{hashA, hashB})
	if err != nil || !cached[hashA] || cached[hashB] {
		t.Errorf("cached %v, err %v", cached, err)
	}

	magnet := "magnet:?xt=urn:btih:" + hashA + "&dn=A%20Name"
	if err := c.Add(magnet); err != nil || len(f.addedForms) != 1 || f.addedForms[0] != magnet {
		t.Errorf("add: err %v, received %v", err, f.addedForms)
	}

	link, err := c.Link(fetch.Item{ID: "41"}, fetch.File{ID: "7"})
	if err != nil || link != f.srv.URL+"/cdn/file" {
		t.Errorf("link %q, err %v", link, err)
	}
	for _, want := range []string{"token=" + key, "torrent_id=41", "file_id=7"} {
		if !strings.Contains(f.linkQuery, want) {
			t.Errorf("link request %q is missing %s", f.linkQuery, want)
		}
	}

	if err := c.Remove(fetch.Item{ID: "900"}); err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 1 || f.deleted[0]["operation"] != "delete" || f.deleted[0]["torrent_id"] != float64(900) {
		t.Errorf("delete body: %v", f.deleted)
	}
	if err := c.Remove(fetch.Item{ID: "not-a-number"}); err == nil {
		t.Error("an id that is not TorBox's should be refused before any request")
	}
}

func TestRetriesAndReportsErrors(t *testing.T) {
	f, c := newFakeAPI(t)
	f.rateLimits = 2
	var waits []time.Duration
	c.Sleep = func(d time.Duration) { waits = append(waits, d) }

	if _, err := c.List(); err != nil {
		t.Fatalf("should succeed after being rate limited twice: %v", err)
	}
	if fmt.Sprint(waits) != "[7s 7s]" {
		t.Errorf("waits = %v, want the server's Retry-After each time", waits)
	}

	c.MaxRetryWait = time.Second
	f.rateLimits, waits = 1, nil
	if _, err := c.List(); err != nil || fmt.Sprint(waits) != "[1s]" {
		t.Errorf("err %v, waits %v: a long Retry-After should be capped", err, waits)
	}

	calls := f.calls
	bad := New("wrong")
	bad.BaseURL, bad.Sleep = f.srv.URL, func(time.Duration) {}
	_, err := bad.List()
	if err == nil || !strings.Contains(err.Error(), "bad key") || !strings.Contains(err.Error(), "403") {
		t.Errorf("want the server's reason in the error, got %v", err)
	}
	if f.calls != calls+1 {
		t.Errorf("a rejected key must not be retried: %d calls", f.calls-calls)
	}

	f.rateLimits = 99
	if _, err := c.List(); err == nil || !strings.Contains(err.Error(), "429") {
		t.Errorf("should give up eventually, got %v", err)
	}

	gone := New(key)
	gone.BaseURL, gone.Retries, gone.Sleep = "http://127.0.0.1:1/"+key, 0, func(time.Duration) {}
	if _, err := gone.List(); err == nil || strings.Contains(err.Error(), key) {
		t.Errorf("a network error must not leak the key: %v", err)
	}
}

// The registered downloader, driven by the shared download code end to end.
func TestRegisteredAndWorksEndToEnd(t *testing.T) {
	if _, err := fetch.NewDownloader("torbox", "  "); err == nil {
		t.Error("an empty key should be refused")
	}
	service, err := fetch.NewDownloader("torbox", key)
	if err != nil || service.Name() != "TorBox" {
		t.Fatalf("downloader %v, err %v", service, err)
	}
	f, c := newFakeAPI(t)
	f.cached[hashA] = true

	opts := fetch.DefaultDownloadOptions()
	opts.Dest, opts.RemoveAfter = t.TempDir(), true
	results, err := fetch.Download(c, []fetch.Magnet{{Hash: hashA, URI: "magnet:?xt=urn:btih:" + hashA}}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if r := results[0]; r.Status != fetch.DownloadDone || r.Files != 1 || !r.Removed {
		t.Errorf("result: %+v", r)
	}
	data, err := os.ReadFile(filepath.Join(opts.Dest, "Added", "file.bin"))
	if err != nil || string(data) != "file body" {
		t.Errorf("file = %q, err %v", data, err)
	}
	if len(f.torrents) != 0 {
		t.Errorf("the item should have been removed from the account: %v", f.torrents)
	}
}
