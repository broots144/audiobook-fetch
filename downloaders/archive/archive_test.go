package archive

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fetch "github.com/broots144/audiobook-fetch"
)

const hash = "64b9bedd5e330c548cd2b8f1c9dbc281b0bcda24"

// Trimmed from a real item; the shape is the Archive's own.
const itemSample = `{"metadata":{"identifier":"frankenstein_1107_librivox","title":"Frankenstein","creator":"Mary Shelley"},"files":[
  {"name":"frankenstein_00_shelley.mp3","source":"original","format":"VBR MP3","size":"31617326"},
  {"name":"frankenstein_00_shelley_64kb.mp3","source":"derivative","format":"64Kbps MP3","size":"15809306"},
  {"name":"frankenstein_00_shelley.ogg","source":"derivative","format":"Ogg Vorbis","size":"14000000"},
  {"name":"frankenstein_00_shelley.png","source":"derivative","format":"PNG","size":"20000"},
  {"name":"frankenstein_00_shelley_spectrogram.png","source":"derivative","format":"Spectrogram","size":"250000"},
  {"name":"frankenstein_01_shelley.mp3","source":"original","format":"VBR MP3","size":"10262830"},
  {"name":"Frankenstein_1107.jpg","source":"original","format":"JPEG","size":"22000"},
  {"name":"__ia_thumb.jpg","source":"original","format":"Item Tile","size":"9000"},
  {"name":"frankenstein_1107_librivox_meta.xml","source":"original","format":"Metadata","size":"3000"},
  {"name":"frankenstein_1107_librivox_archive.torrent","source":"metadata","format":"Archive BitTorrent","btih":"64b9bedd5e330c548cd2b8f1c9dbc281b0bcda24","size":"58780"}
]}`

func TestParseItemKeepsTheOriginalAudioAndTheCover(t *testing.T) {
	item, err := ParseItem(itemSample)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range item.Files {
		names = append(names, f.Path)
	}
	if got := strings.Join(names, " "); got != "frankenstein_00_shelley.mp3 frankenstein_01_shelley.mp3 Frankenstein_1107.jpg" {
		t.Errorf("files: %s", got)
	}
	if item.Name != "Frankenstein - Mary Shelley" || !item.Ready || item.Files[0].Size != 31617326 {
		t.Errorf("item: %+v", item)
	}

	// With no original audio, the audio that is listed is used.
	item, err = ParseItem(`{"metadata":{"title":"Odd One","creator":["First","Second"]},"files":[
	  {"name":"a_64kb.mp3","source":"derivative","format":"64Kbps MP3","size":"100"},
	  {"name":"a.flac.txt","source":"original","format":"Text","size":"5"}]}`)
	if err != nil || len(item.Files) != 1 || item.Files[0].Path != "a_64kb.mp3" || item.Name != "Odd One - First" {
		t.Errorf("fallback: %+v, %v", item, err)
	}
	if _, err := ParseItem(`{"metadata":{"title":"A Scanned Book"},"files":[{"name":"book.pdf","source":"original","size":"100"}]}`); err == nil {
		t.Error("an item with no audio has nothing to fetch")
	}
	if _, err := ParseItem("not json"); err == nil {
		t.Error("metadata that is not JSON should be an error")
	}
}

func TestHandlesOnlyTheArchivesOwnLinks(t *testing.T) {
	theirs := fetch.BuildMagnet(hash, "Frankenstein", []string{"http://bt1.archive.org:6969/announce"})
	if !Handles(theirs) {
		t.Errorf("an Archive link should be handled: %s", theirs)
	}
	if Handles(fetch.BuildMagnet(hash, "A book about archive.org", nil)) {
		t.Error("a link from anywhere else is not, whatever its name says")
	}
}

func TestTheWholeRoundTrip(t *testing.T) {
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path+"?"+r.URL.Query().Get("q"))
		switch {
		case r.URL.Path == "/advancedsearch.php" && r.URL.Query().Get("q") == "btih:"+hash:
			_, _ = io.WriteString(w, `{"response":{"docs":[{"identifier":"frankenstein_1107_librivox"}]}}`)
		case r.URL.Path == "/advancedsearch.php":
			_, _ = io.WriteString(w, `{"response":{"docs":[]}}`)
		case r.URL.Path == "/metadata/frankenstein_1107_librivox":
			_, _ = io.WriteString(w, itemSample)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("ARCHIVE_URL", server.URL)

	service, err := fetch.NewDownloader("archive", "") // no key: there is no account
	if err != nil {
		t.Fatal(err)
	}
	if service.Name() != "Internet Archive" {
		t.Errorf("name: %q", service.Name())
	}
	if cached, _ := service.CheckCached([]string{strings.ToUpper(hash)}); !cached[hash] {
		t.Error("everything the Archive has can be served at once")
	}
	if items, _ := service.List(); len(items) != 0 {
		t.Errorf("nothing is held before anything is added: %v", items)
	}
	if err := service.Add(fetch.BuildMagnet(hash, "Frankenstein", nil)); err != nil {
		t.Fatal(err)
	}
	items, _ := service.List()
	if len(items) != 1 || items[0].Hash != hash || items[0].ID != "frankenstein_1107_librivox" || len(items[0].Files) != 3 {
		t.Fatalf("items: %+v", items)
	}
	link, _ := service.Link(items[0], fetch.File{ID: "disc 1/track #1.mp3"})
	if want := server.URL + "/download/frankenstein_1107_librivox/disc%201/track%20%231.mp3"; link != want {
		t.Errorf("link %q, want %q", link, want)
	}
	if err := service.Remove(items[0]); err != nil {
		t.Fatal(err)
	}
	if items, _ = service.List(); len(items) != 0 {
		t.Errorf("after removing: %v", items)
	}

	other := strings.Repeat("ab", 20)
	if err := service.Add(fetch.BuildMagnet(other, "x", nil)); err == nil || !strings.Contains(err.Error(), "no item with the info hash") {
		t.Errorf("a hash the Archive does not know: %v", err)
	}
	if err := service.Add("not a link"); err == nil {
		t.Error("only magnet links are taken")
	}
}
