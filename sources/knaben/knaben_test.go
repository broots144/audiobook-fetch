package knaben

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	fetch "github.com/broots144/audiobook-fetch"
)

const (
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

const sample = `{"total": {"value": 4}, "hits": [
  {"title": "Mary Shelley - Frankenstein (Unabridged) [M4B 64kbps]", "hash": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
   "magnetUrl": "magnet:?xt=urn:btih:aaaa&dn=x&tr=udp%3A%2F%2Ft.example%3A1%2Fannounce&tr=http%3A%2F%2Fu.example%2Fannounce",
   "details": "https://tracker.example/1", "tracker": "One", "date": "2014-03-14T23:00:00+00:00", "bytes": 264461352, "seeders": 4},
  {"title": "Frankenstein", "hash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "details": "https://other.example/9", "seeders": 11},
  {"title": "Frankenstein.Audiobook.MP3", "hash": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "details": "", "seeders": 0},
  {"title": "No hash, nothing to download", "hash": null, "details": "https://tracker.example/3", "seeders": 50}
]}`

func TestParse(t *testing.T) {
	found, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("got %d candidates, want the two distinct hashes", len(found))
	}
	a, b := found[0], found[1]
	if a.InfoHash != hashA || a.Heading != "Mary Shelley - Frankenstein" || a.Edition != "Unabridged" ||
		a.Format != "M4B" || a.Bitrate != "64 Kbps" || a.Size != "252.2 MB" || a.Posted != "2014-03-14" ||
		a.URL != "https://tracker.example/1" || len(a.Trackers) != 2 || a.Trackers[0] != "udp://t.example:1/announce" {
		t.Errorf("first candidate: %+v", a)
	}
	if a.Seeders == nil || *a.Seeders != 11 {
		t.Errorf("the same files on two trackers should keep the higher seeder count: %v", a.Seeders)
	}
	if b.InfoHash != hashB || b.Heading != "Frankenstein" || b.Format != "MP3" || b.URL != "knaben:"+hashB || !b.Unseeded() {
		t.Errorf("second candidate: %+v", b)
	}
	if none, err := Parse(`{"hits": []}`); err != nil || len(none) != 0 {
		t.Errorf("no hits: %v, %v", none, err)
	}
	if _, err := Parse("<html>down for maintenance</html>"); err == nil {
		t.Error("an answer that is not JSON should be an error")
	}
}

func TestSearchAsksForAudiobooksWithEveryWord(t *testing.T) {
	var got request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || json.Unmarshal(body, &got) != nil {
			t.Errorf("want a JSON POST, got %s %s", r.Method, body)
		}
		_, _ = io.WriteString(w, sample)
	}))
	defer server.Close()
	t.Setenv("KNABEN_API_URL", server.URL)

	site, err := New(fetch.SourceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	found, err := site.Search("Frankenstein Shelley", 2)
	if err != nil || len(found) != 2 {
		t.Fatalf("found %d, err %v", len(found), err)
	}
	if got.Query != "Frankenstein Shelley" || got.SearchType != "100%" || got.From != 50 ||
		len(got.Categories) != 1 || got.Categories[0] != 1003000 {
		t.Errorf("request: %+v", got)
	}
	if err := site.Details(found[0]); err != nil || found[0].InfoHash != hashA {
		t.Errorf("Details should leave a complete candidate alone: %v", err)
	}
}
