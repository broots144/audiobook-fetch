package archive

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fetch "github.com/broots144/audiobook-fetch"
)

// Trimmed from real answers; the shapes are the Archive's own.
const searchSample = `{"responseHeader":{"status":0},"response":{"numFound":4,"start":0,"docs":[
  {"creator":"Mary Shelley","identifier":"frankenstein_1107_librivox","item_size":1111668207,"language":"eng",
   "publicdate":"2011-07-20T05:23:52Z","title":"Frankenstein"},
  {"creator":["Mary Wollstonecraft Shelley","Another Hand"],"identifier":"frankenstein_de_1211_librivox","item_size":1176928450,
   "language":["deu","eng"],"title":"Frankenstein oder Der moderne Prometheus"},
  {"identifier":"anonymous_work_librivox","title":"A Work With No Author (version 3)","language":"xyz"},
  {"identifier":"","title":"No identifier, nothing to fetch"}
]}}`

const itemSample = `{"metadata":{"identifier":"frankenstein_1107_librivox","title":"Frankenstein"},"files":[
  {"name":"frankenstein_00_shelley.mp3","format":"VBR MP3","size":"31617326"},
  {"name":"frankenstein_1107_librivox_files_all.torrent","format":"BitTorrent","btih":null},
  {"name":"frankenstein_1107_librivox_archive.torrent","format":"Archive BitTorrent","btih":"64B9BEDD5E330C548CD2B8F1C9DBC281B0BCDA24"}
]}`

func TestParseSearch(t *testing.T) {
	found, err := ParseSearch(searchSample)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 3 {
		t.Fatalf("got %d candidates, want the three with an identifier", len(found))
	}
	a, b, c := found[0], found[1], found[2]
	if a.Heading != "Frankenstein - Mary Shelley" || a.URL != "frankenstein_1107_librivox" || a.Language != "English" ||
		a.Format != "MP3" || a.Edition != "Unabridged" || a.Size != "1.0 GB" || a.Posted != "2011-07-20" ||
		len(a.Authors) != 1 || a.Authors[0] != "Mary Shelley" || a.InfoHash != "" {
		t.Errorf("first candidate: %+v", a)
	}
	if b.Language != "German" || len(b.Authors) != 2 || b.Heading != "Frankenstein oder Der moderne Prometheus - Mary Wollstonecraft Shelley, Another Hand" {
		t.Errorf("a field given as a list: %+v", b)
	}
	if c.Heading != "A Work With No Author" || c.Language != "xyz" || len(c.Authors) != 0 || c.Size != "" {
		t.Errorf("a sparse record: %+v", c)
	}
	if none, err := ParseSearch(`{"response":{"docs":[]}}`); err != nil || len(none) != 0 {
		t.Errorf("no hits: %v, %v", none, err)
	}
	if none, err := ParseSearch(""); err != nil || none != nil {
		t.Errorf("an empty answer: %v, %v", none, err)
	}
	if _, err := ParseSearch("<html>down for maintenance</html>"); err == nil {
		t.Error("an answer that is not JSON should be an error")
	}
}

func TestParseItem(t *testing.T) {
	c := &fetch.Candidate{}
	if err := ParseItem(itemSample, c); err != nil {
		t.Fatal(err)
	}
	if c.InfoHash != "64b9bedd5e330c548cd2b8f1c9dbc281b0bcda24" || len(c.Trackers) != 2 || !strings.Contains(c.Trackers[0], "archive.org") {
		t.Errorf("candidate: %+v", c)
	}
	if err := ParseItem(`{"files":[{"name":"a.mp3"}]}`, &fetch.Candidate{}); err == nil {
		t.Error("an item with no torrent has nothing to download")
	}
	if err := ParseItem("not json", &fetch.Candidate{}); err == nil {
		t.Error("metadata that is not JSON should be an error")
	}
}

func TestSearchAndDetails(t *testing.T) {
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path+"?"+r.URL.Query().Get("q")+"&page="+r.URL.Query().Get("page"))
		switch r.URL.Path {
		case "/advancedsearch.php":
			_, _ = io.WriteString(w, searchSample)
		case "/metadata/frankenstein_1107_librivox":
			_, _ = io.WriteString(w, itemSample)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("ARCHIVE_URL", server.URL+"/")

	site, err := New(fetch.SourceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if site.Name() != "Internet Archive" {
		t.Errorf("name: %q", site.Name())
	}
	found, err := site.Search(`Frankenstein: "Shelley" (1818)`, 2)
	if err != nil || len(found) != 3 {
		t.Fatalf("found %d, err %v", len(found), err)
	}
	// Only the free, public-domain collection is searched, and characters
	// that mean something to the search language are taken out.
	if want := "/advancedsearch.php?collection:librivoxaudio AND (Frankenstein AND Shelley AND 1818)&page=2"; asked[0] != want {
		t.Errorf("asked %q, want %q", asked[0], want)
	}
	if found[0].URL != server.URL+"/details/frankenstein_1107_librivox" {
		t.Errorf("URL: %s", found[0].URL)
	}
	if err := site.Details(found[0]); err != nil || found[0].InfoHash != "64b9bedd5e330c548cd2b8f1c9dbc281b0bcda24" {
		t.Errorf("Details: %v, %+v", err, found[0])
	}
	// Asked again, it has nothing left to fetch.
	before := len(asked)
	if err := site.Details(found[0]); err != nil || len(asked) != before {
		t.Errorf("a complete candidate should be left alone: %v", err)
	}
	if err := site.Details(&fetch.Candidate{URL: "https://elsewhere.example/x"}); err == nil {
		t.Error("a candidate from somewhere else is refused")
	}
	if none, err := site.Search(" :: ", 1); err != nil || none != nil {
		t.Errorf("nothing to search for: %v, %v", none, err)
	}
}
