package piratebay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fetch "github.com/broots144/audiobook-fetch"
)

const hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

const sample = `[
  {"id":"11","name":"Frankenstein - Mary Shelley [Unabridged] mp3 128k","info_hash":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
   "leechers":"1","seeders":"5","num_files":"22","size":"133614669","username":"u","added":"1394838000","status":"vip","category":"102","imdb":""},
  {"id":"12","name":"Frankenstein (1931) 1080p","info_hash":"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
   "leechers":"9","seeders":"400","num_files":"1","size":"9","username":"u","added":"1","status":"vip","category":"207","imdb":""}
]`

const nothing = `[{"id":"0","name":"No results returned","info_hash":"0000000000000000000000000000000000000000",
  "leechers":"0","seeders":"0","num_files":"0","size":"0","username":"","added":"0","status":"member","category":"0","imdb":""}]`

func TestParse(t *testing.T) {
	found, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("got %d candidates, want only the audiobook", len(found))
	}
	c := found[0]
	if c.InfoHash != hashA || c.Heading != "Frankenstein - Mary Shelley" || c.Edition != "Unabridged" ||
		c.Format != "MP3" || c.Bitrate != "128 Kbps" || c.Size != "127.4 MB" || c.Posted != "2014-03-14" ||
		c.URL != DescriptionURL+"11" || c.Seeders == nil || *c.Seeders != 5 {
		t.Errorf("candidate: %+v", c)
	}
	if none, err := Parse(nothing); err != nil || len(none) != 0 {
		t.Errorf("the 'no results' placeholder is not a listing: %v, %v", none, err)
	}
	if _, err := Parse("<html>blocked</html>"); err == nil {
		t.Error("an answer that is not JSON should be an error")
	}
}

func TestSearchAsksAgainWithoutTheCategoryWhenItComesBackEmpty(t *testing.T) {
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RawQuery)
		if r.URL.Query().Get("cat") != "" {
			_, _ = io.WriteString(w, nothing)
			return
		}
		_, _ = io.WriteString(w, sample)
	}))
	defer server.Close()
	t.Setenv("PIRATEBAY_API_URL", server.URL)

	site, err := New(fetch.SourceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	found, err := site.Search("Frankenstein Shelley", 1)
	if err != nil || len(found) != 1 || found[0].InfoHash != hashA {
		t.Fatalf("found %+v, err %v", found, err)
	}
	if strings.Join(asked, " | ") != "q=Frankenstein+Shelley&cat=102 | q=Frankenstein+Shelley" {
		t.Errorf("asked %v", asked)
	}
	if more, _ := site.Search("Frankenstein Shelley", 2); len(more) != 0 || len(asked) != 2 {
		t.Error("there is no second page to ask for")
	}
}
