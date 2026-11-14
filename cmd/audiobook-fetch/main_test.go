package main

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	fetch "github.com/broots144/audiobook-fetch"
)

// The commands are run in-process against a fake source ("fakesite") and a
// fake download service ("fakebox"), registered the way real plugins are.
// Nothing here touches the network, a real account or the real config.

const (
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hashC = "cccccccccccccccccccccccccccccccccccccccc"
)

func magnet(hash string) string { return "magnet:?xt=urn:btih:" + hash + "&dn=x" }

// fakeSite answers searches from a fixed set of listings: a listing is a hit
// when its heading contains every word of the query.
type fakeSite struct {
	name     string
	listings []fetch.Candidate
	fail     bool
	searches int
}

func (f *fakeSite) Name() string { return f.name }

func (f *fakeSite) Search(query string, page int) ([]*fetch.Candidate, error) {
	f.searches++
	if f.fail {
		return nil, errors.New("the site is down")
	}
	if page > 1 {
		return nil, nil
	}
	var hits []*fetch.Candidate
	for _, listing := range f.listings {
		match := true
		for _, word := range strings.Fields(strings.ToLower(query)) {
			match = match && strings.Contains(strings.ToLower(listing.Heading), word)
		}
		if match {
			c := listing
			hits = append(hits, &c)
		}
	}
	return hits, nil
}

func (f *fakeSite) Details(*fetch.Candidate) error { return nil }

func listing(hash, heading string) fetch.Candidate {
	return fetch.Candidate{Heading: heading, URL: "https://fakesite.example/" + hash, Language: "English",
		Format: "MP3", Bitrate: "64 Kbps", InfoHash: hash}
}

// fakeBox holds every link it is given at once, and serves the files over a
// local HTTP server.
type fakeBox struct {
	mu      sync.Mutex
	server  *httptest.Server
	catalog map[string]map[string]string // hash -> path -> content
	items   map[string]fetch.Item
	content map[string]string // file id -> content
	key     string
	added   []string
}

func (f *fakeBox) Name() string { return "FakeBox" }

func (f *fakeBox) List() ([]fetch.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var items []fetch.Item
	for _, item := range f.items {
		items = append(items, item)
	}
	return items, nil
}

func (f *fakeBox) CheckCached(hashes []string) (map[string]bool, error) {
	cached := map[string]bool{}
	for _, hash := range hashes {
		cached[hash] = f.catalog[hash] != nil
	}
	return cached, nil
}

func (f *fakeBox) Add(link string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	magnets, _ := fetch.ParseMagnets(link)
	if len(magnets) != 1 {
		return errors.New("not a magnet link")
	}
	hash := magnets[0].Hash
	f.added = append(f.added, hash)
	item := fetch.Item{ID: "item-" + hash[:4], Hash: hash, Name: "Item " + hash[:4], Ready: true, State: "cached", Progress: 1}
	for path, content := range f.catalog[hash] {
		id := fmt.Sprintf("%s-%d", hash[:4], len(f.content))
		f.content[id] = content
		item.Files = append(item.Files, fetch.File{ID: id, Path: path, Size: int64(len(content))})
	}
	f.items[hash] = item
	return nil
}

func (f *fakeBox) Link(_ fetch.Item, file fetch.File) (string, error) {
	return f.server.URL + "/" + file.ID, nil
}

func (f *fakeBox) Remove(item fetch.Item) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.items, item.Hash)
	return nil
}

var (
	site  = &fakeSite{name: "FakeSite"}
	other = &fakeSite{name: "OtherSite"}
	box   *fakeBox
)

func init() {
	fetch.RegisterSource("fakesite", func(fetch.SourceConfig) (fetch.Source, error) { return site, nil })
	fetch.RegisterSource("othersite", func(fetch.SourceConfig) (fetch.Source, error) { return other, nil })
	fetch.RegisterDownloader("fakebox", func(key string) (fetch.Downloader, error) {
		if box == nil {
			return nil, errors.New("no fake service in this test")
		}
		if key == "" {
			return nil, errors.New("FakeBox needs an API key")
		}
		box.key = key
		return box, nil
	})
}

// sandbox points the tool at an empty home folder, a fresh fake source and
// download service, and a PATH holding only a stand-in ffprobe, so a test
// sees none of the machine's real config, key, or audio tools.
func sandbox(t *testing.T) (home string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in ffprobe is a shell script")
	}
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WHISPER_MODEL", "")
	t.Setenv("FAKEBOX_API_KEY", "")

	tools := t.TempDir()
	// Every MP3 is an hour long; an M4B is two hours.
	script := `#!/bin/sh
for arg; do last=$arg; done
case "$last" in
*.m4b) echo '{"streams":[{"codec_name":"aac","codec_type":"audio"}],"format":{"duration":"7200.0","bit_rate":"64000"}}' ;;
*) echo '{"streams":[{"codec_name":"mp3","codec_type":"audio"}],"format":{"duration":"3600.0","bit_rate":"64000"}}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(tools, "ffprobe"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools)

	*site = fakeSite{name: "FakeSite", listings: []fetch.Candidate{
		listing(hashA, "Frankenstein - Mary Shelley"),
		listing(hashB, "Dracula - Bram Stoker"),
	}}
	*other = fakeSite{name: "OtherSite"}
	box = &fakeBox{items: map[string]fetch.Item{}, content: map[string]string{}, catalog: map[string]map[string]string{
		hashA: {"Frankenstein/01.mp3": "frank-1", "Frankenstein/02.mp3": "frank-2"},
		hashB: {"Dracula/01.mp3": "drac-1"},
		hashC: {"Loose Book/readme.txt": "no audio here"},
	}}
	box.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		box.mu.Lock()
		content, ok := box.content[strings.TrimPrefix(r.URL.Path, "/")]
		box.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(content))
	}))
	t.Cleanup(func() { box.server.Close(); box = nil })
	return home
}

// capture runs fn and returns what it printed.
func capture(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	dir := t.TempDir()
	open := func(name string) *os.File {
		file, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return file
	}
	out, errOut := open("stdout"), open("stderr")
	realOut, realErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = out, errOut
	defer func() { os.Stdout, os.Stderr = realOut, realErr }()
	fn()
	out.Close()
	errOut.Close()
	read := func(name string) string {
		data, _ := os.ReadFile(filepath.Join(dir, name))
		return string(data)
	}
	return read("stdout"), read("stderr")
}

func write(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func read(t *testing.T, parts ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// report reads results.csv as one map per row, keyed by column name.
func report(t *testing.T, dir string) []map[string]string {
	t.Helper()
	rows, err := csv.NewReader(strings.NewReader(read(t, dir, "results.csv"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for _, row := range rows[1:] {
		line := map[string]string{}
		for i, name := range rows[0] {
			line[name] = row[i]
		}
		out = append(out, line)
	}
	return out
}

// testConfig is what loadConfig gives a machine with no config file, but
// pointed at the fakes.
func testConfig() config {
	return config{Sources: []string{"fakesite"}, Downloader: "fakebox"}
}

func TestParseArgsTakesFlagsAnywhere(t *testing.T) {
	for _, args := range [][]string{
		{"-quiet", "-n", "3", "list.txt", "folder"},
		{"list.txt", "folder", "-quiet", "-n", "3"},
		{"list.txt", "-n=3", "folder", "-quiet"},
	} {
		flags := flag.NewFlagSet("test", flag.ContinueOnError)
		quiet := flags.Bool("quiet", false, "")
		n := flags.Int("n", 0, "")
		positional := parseArgs(flags, args)
		if !*quiet || *n != 3 || strings.Join(positional, " ") != "list.txt folder" {
			t.Errorf("%v: quiet=%v n=%d positional=%v", args, *quiet, *n, positional)
		}
	}
}

func TestDestinationPrefersTheCommandLine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got, _ := destination([]string{"list", "named"}, 1, "configured"); got != "named" {
		t.Errorf("a folder on the command line wins, got %q", got)
	}
	if got, _ := destination([]string{"list"}, 1, "configured"); got != "configured" {
		t.Errorf("then -to or the config, got %q", got)
	}
	if _, err := destination([]string{"list"}, 1, ""); err == nil || !strings.Contains(err.Error(), "config.json") {
		t.Errorf("no folder anywhere should say where to set one: %v", err)
	}
}

func TestReadKey(t *testing.T) {
	home := sandbox(t)
	keyFile := filepath.Join(home, ".config", fetch.AppName, "fakebox.key")

	if _, err := readKey("fakebox", ""); err == nil || !strings.Contains(err.Error(), "FAKEBOX_API_KEY") || !strings.Contains(err.Error(), keyFile) {
		t.Errorf("a missing key should name the variable and the file: %v", err)
	}

	write(t, keyFile, "  \n")
	if _, err := readKey("fakebox", ""); err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Errorf("an empty key file: %v", err)
	}

	write(t, keyFile, "from-file\n")
	var key string
	_, warned := capture(t, func() { key, _ = readKey("fakebox", "") })
	if key != "from-file" || warned != "" {
		t.Errorf("key file: %q, warning %q", key, warned)
	}

	// A key other users can read is used, but not silently.
	loose := write(t, filepath.Join(home, "loose.key"), "loose-key")
	if err := os.Chmod(loose, 0o644); err != nil {
		t.Fatal(err)
	}
	_, warned = capture(t, func() { key, _ = readKey("fakebox", loose) })
	if key != "loose-key" || !strings.Contains(warned, "chmod 600") {
		t.Errorf("loose key file: %q, warning %q", key, warned)
	}
	if strings.Contains(warned, "loose-key") {
		t.Errorf("the warning must not show the key: %q", warned)
	}

	t.Setenv("FAKEBOX_API_KEY", " from-env ")
	if key, _ = readKey("fakebox", ""); key != "from-env" {
		t.Errorf("the environment wins over the file, got %q", key)
	}
}

func TestLoadConfig(t *testing.T) {
	home := sandbox(t)
	cfg := loadConfig()
	if cfg.Downloader != defaultDownloader || strings.Join(cfg.Sources, ",") != strings.Join(defaultSources, ",") || cfg.Check != nil {
		t.Errorf("with no config file the defaults apply: %+v", cfg)
	}

	path := filepath.Join(home, ".config", fetch.AppName, "config.json")
	write(t, path, `{"to": "/books", "remove_after": true, "sources": [], "check": false, "whisper_model": "/m.bin"}`)
	cfg = loadConfig()
	if cfg.To != "/books" || !cfg.RemoveAfter || cfg.Check == nil || *cfg.Check || cfg.WhisperModel != "/m.bin" {
		t.Errorf("config file: %+v", cfg)
	}
	if cfg.Downloader != defaultDownloader || len(cfg.Sources) != len(defaultSources) {
		t.Errorf("keys left out or empty fall back to the defaults: %+v", cfg)
	}

	// The config's values become the options' defaults.
	var download downloadFlags
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	download.register(flags, cfg)
	if download.opts.Dest != "/books" || !download.opts.RemoveAfter || download.check || download.model != "/m.bin" {
		t.Errorf("options from config: %+v", download)
	}
	_ = flags.Parse([]string{"-check", "-to", "/elsewhere"})
	if !download.check || download.opts.Dest != "/elsewhere" {
		t.Errorf("the command line overrides the config: %+v", download)
	}
}

func TestDefaultModelLooksInTheConfigFolder(t *testing.T) {
	home := sandbox(t)
	if got := defaultModel(config{}); got != "" {
		t.Errorf("no model anywhere, got %q", got)
	}
	found := write(t, filepath.Join(home, ".config", fetch.AppName, "models", "ggml-base.bin"), "model")
	if got := defaultModel(config{}); got != found {
		t.Errorf("a model in the config folder is found, got %q", got)
	}
	t.Setenv("WHISPER_MODEL", "/from/env.bin")
	if got := defaultModel(config{}); got != "/from/env.bin" {
		t.Errorf("WHISPER_MODEL comes before it, got %q", got)
	}
	if got := defaultModel(config{WhisperModel: "/from/config.bin"}); got != "/from/config.bin" {
		t.Errorf("and the config before that, got %q", got)
	}
}

func TestNewCheckerUnderstandsAny(t *testing.T) {
	sandbox(t)
	if c := newChecker("ANY", ""); c.Language != "" {
		t.Errorf(`"any" accepts every language, got %q`, c.Language)
	}
	c := newChecker("german", "")
	if c.Language != "german" || c.FFprobe == "" || c.Model != "" {
		t.Errorf("checker: %+v", c)
	}
}

func TestUsageNamesThePluginsAndTheLegalNotice(t *testing.T) {
	sandbox(t)
	var out bytes.Buffer
	usage(&out)
	for _, want := range []string{"fakesite", "audiobookbay", "fakebox", "torbox", "check [FOLDER]", "legal right to download"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage should mention %q:\n%s", want, out.String())
		}
	}
}

func TestSearchFlagsRefuseUnknownOrNoSources(t *testing.T) {
	sandbox(t)
	for names, want := range map[string]string{"nosuchsite": "unknown source", " , ": "no source to search"} {
		var search searchFlags
		flags := flag.NewFlagSet("test", flag.ContinueOnError)
		search.register(flags, testConfig())
		_ = flags.Parse([]string{"-source", names})
		if err := search.finish(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("-source %q: %v", names, err)
		}
	}

	var search searchFlags
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	search.register(flags, testConfig())
	_ = flags.Parse([]string{"-source", "fakesite, othersite", "-lang", "Any"})
	if err := search.finish(); err != nil || len(search.sources) != 2 || search.opts.Language != "" {
		t.Errorf("two sources, any language: %v, %+v", err, search)
	}
}

func TestPlainCellsDefusesFormulas(t *testing.T) {
	got := plainCells([]string{"=HYPERLINK(1)", "+1", "-1", "@x", "Dune - Frank Herbert", "0.95", ""})
	want := []string{"'=HYPERLINK(1)", "'+1", "'-1", "'@x", "Dune - Frank Herbert", "0.95", ""}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q", got)
	}
}

func TestActionable(t *testing.T) {
	matched := fetch.Result{Status: fetch.StatusMatched, Magnet: magnet(hashA)}
	low := fetch.Result{Status: fetch.StatusLowConfidence, Magnet: magnet(hashA)}
	if !actionable(matched, false) || actionable(low, false) || !actionable(low, true) {
		t.Error("a match is acted on; a low-confidence pick only when asked")
	}
	if actionable(fetch.Result{Status: fetch.StatusMatched}, true) || actionable(fetch.Result{Status: fetch.StatusNotFound, Magnet: "x"}, true) {
		t.Error("nothing without a link, and nothing that was not found")
	}
}

func TestWriteReport(t *testing.T) {
	dir := t.TempDir()
	bundle := listing(hashA, "=Two Books In One")
	bundle.Authors, bundle.Source = []string{"A. Writer", "B. Writer"}, "FakeSite"
	results := []fetch.Result{
		{Wanted: fetch.Wanted{Raw: "Book One"}, Status: fetch.StatusMatched, Score: 0.91, Candidate: &bundle, Magnet: magnet(hashA),
			RunnersUp: []fetch.RunnerUp{{Score: 0.5, Heading: "Another"}}},
		{Wanted: fetch.Wanted{Raw: "Book Two"}, Status: fetch.StatusMatched, Score: 0.9, Candidate: &bundle, Magnet: magnet(hashA)},
		{Wanted: fetch.Wanted{Raw: "Unsure"}, Status: fetch.StatusLowConfidence, Score: 0.6, Candidate: &fetch.Candidate{InfoHash: hashB}, Magnet: magnet(hashB)},
		{Wanted: fetch.Wanted{Raw: "Nowhere"}, Status: fetch.StatusNotFound, Note: "no listings"},
	}
	downloads := map[string]fetch.DownloadResult{hashA: {Status: fetch.DownloadDone}, hashB: {Status: fetch.DownloadDone}}
	if err := writeReport(dir, results, false, downloads); err != nil {
		t.Fatal(err)
	}

	if got := read(t, dir, "magnets.txt"); got != magnet(hashA)+"\n" {
		t.Errorf("a listing two books share is listed once, and unsure picks not at all:\n%s", got)
	}
	rows := report(t, dir)
	if len(rows) != 4 {
		t.Fatalf("every line of the list gets a row, got %d", len(rows))
	}
	first := rows[0]
	if first["input"] != "Book One" || first["download"] != fetch.DownloadDone || first["score"] != "0.91" ||
		first["authors"] != "A. Writer, B. Writer" || first["runners_up"] != "0.50 Another" || first["source"] != "FakeSite" {
		t.Errorf("first row: %v", first)
	}
	if first["matched"] != "'=Two Books In One" {
		t.Errorf("a heading that looks like a formula must be written as text, got %q", first["matched"])
	}
	if rows[2]["download"] != "" || rows[3]["note"] != "no listings" || rows[3]["magnet"] != "" {
		t.Errorf("a pick that was not acted on has no download status: %v / %v", rows[2], rows[3])
	}

	// Only confident matches are carried over to the next run.
	previous := loadMatches(filepath.Join(dir, "results.json"))
	if len(previous) != 2 || previous["Book One"].Candidate.InfoHash != hashA {
		t.Errorf("reusable matches: %v", previous)
	}
	write(t, filepath.Join(dir, "results.json"), "not json")
	if got := loadMatches(filepath.Join(dir, "results.json")); len(got) != 0 {
		t.Errorf("a damaged report is ignored, got %v", got)
	}
	if got := loadMatches(filepath.Join(dir, "missing.json")); len(got) != 0 {
		t.Errorf("no earlier report, got %v", got)
	}
}

func TestProgressLines(t *testing.T) {
	c := listing(hashA, "Dune - Frank Herbert")
	c.Edition, c.Size = "Unabridged", "500 MB"
	_, got := capture(t, func() {
		progress(1, 2, fetch.Result{Wanted: fetch.Wanted{Raw: "Dune"}, Status: fetch.StatusMatched, Score: 0.97, Candidate: &c,
			Magnet: magnet(hashA), Note: "next-best listing"}, true)
		progress(2, 2, fetch.Result{Wanted: fetch.Wanted{Raw: "Nothing"}, Status: fetch.StatusNotFound, Note: "no listings"}, false)
	})
	for _, want := range []string{"[1/2] ✓ 0.97  Dune", "→ Dune - Frank Herbert  (Unabridged, MP3, 500 MB)  [next-best listing]  (from an earlier run)",
		"[2/2] ✗ 0.00  Nothing\n        no listings"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFindCommandWritesAReportAndReusesIt(t *testing.T) {
	sandbox(t)
	work := t.TempDir()
	list := write(t, filepath.Join(work, "books.txt"), "Frankenstein | Mary Shelley\nNo Such Book | Nobody Atall\n")
	out := filepath.Join(work, "report")
	args := []string{list, "-out", out, "-json", "-delay", "0"}

	stdout, stderr := capture(t, func() { cmdFind(args, testConfig()) })
	var results []fetch.Result
	if err := json.Unmarshal([]byte(stdout), &results); err != nil {
		t.Fatalf("-json should print the results on stdout: %v\n%s", err, stdout)
	}
	if len(results) != 2 || results[0].Status != fetch.StatusMatched || results[0].Candidate.InfoHash != hashA ||
		results[0].Candidate.Source != "FakeSite" || results[1].Status != fetch.StatusNotFound {
		t.Errorf("results: %+v", results)
	}
	if !strings.Contains(stderr, "2 of 2 books looked up: 1 matched, 0 low confidence, 1 not found, 0 errors") {
		t.Errorf("summary:\n%s", stderr)
	}
	if got := read(t, out, "magnets.txt"); !strings.Contains(got, hashA) || strings.Count(got, "magnet:") != 1 {
		t.Errorf("magnets.txt:\n%s", got)
	}
	if rows := report(t, out); len(rows) != 2 || rows[0]["status"] != fetch.StatusMatched || rows[1]["status"] != fetch.StatusNotFound {
		t.Errorf("results.csv: %v", rows)
	}

	// A second run asks the source only about the book it did not match.
	site.searches = 0
	_, stderr = capture(t, func() { cmdFind(append(args, "-quiet"), testConfig()) })
	matchedSearches := site.searches
	if strings.Contains(stderr, "[1/2]") || !strings.Contains(stderr, "1 matched") {
		t.Errorf("-quiet prints only the summary:\n%s", stderr)
	}
	site.searches = 0
	capture(t, func() { cmdFind(append(args, "-fresh"), testConfig()) })
	if matchedSearches == 0 || site.searches <= matchedSearches {
		t.Errorf("a rerun should search less than a -fresh one: %d vs %d searches", matchedSearches, site.searches)
	}
}

func TestFindStopsWhenTheSourceKeepsFailing(t *testing.T) {
	sandbox(t)
	site.fail = true
	var search searchFlags
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	search.register(flags, testConfig())
	if err := search.finish(); err != nil {
		t.Fatal(err)
	}
	books, _ := fetch.ParseList("One | A\nTwo | B\nThree | C\nFour | D\nFive | E\n")
	dir := t.TempDir()
	var results []fetch.Result
	var stopped bool
	_, stderr := capture(t, func() { results, stopped = findBooks(books, dir, &search, true) })
	if !stopped || len(results) != 0 {
		t.Errorf("after %d failures in a row the run stops and keeps none of them: stopped=%v, %d results", giveUpAfter, stopped, len(results))
	}
	if !strings.Contains(stderr, "stopped early") || !strings.Contains(stderr, "carry on from book 1") {
		t.Errorf("it should say how to carry on:\n%s", stderr)
	}
	if rows := report(t, dir); len(rows) != 0 {
		t.Errorf("failures are left out of the report so the next run retries them: %v", rows)
	}
}

func TestDownloadCommand(t *testing.T) {
	home := sandbox(t)
	dest := filepath.Join(home, "library")
	links := write(t, filepath.Join(home, "magnets.txt"),
		"# my links\n"+magnet(hashA)+"\nnot a link\n"+magnet(hashA)+"\n"+magnet(hashC)+"\n")
	t.Setenv("FAKEBOX_API_KEY", "the-key")
	args := []string{links, dest, "-json", "-poll", "1ms"}

	stdout, stderr := capture(t, func() { cmdDownload(args, testConfig()) })
	var results []fetch.DownloadResult
	if err := json.Unmarshal([]byte(stdout), &results); err != nil {
		t.Fatalf("-json should print the results on stdout: %v\n%s", err, stdout)
	}
	if box.key != "the-key" || len(box.added) != 2 {
		t.Errorf("the service gets the key and each link once: key %q, added %v", box.key, box.added)
	}
	if len(results) != 2 || results[0].Status != fetch.DownloadDone || results[0].Files != 2 ||
		results[0].Check == nil || results[0].Check.Status != fetch.VerdictOK {
		t.Fatalf("results: %+v", results)
	}
	if got := read(t, dest, "Frankenstein", "02.mp3"); got != "frank-2" {
		t.Errorf("downloaded file holds %q", got)
	}

	// The item with no audio in it is rejected and moved out of the library.
	if results[1].Status != fetch.DownloadRejected || !strings.Contains(results[1].Note, "no audio files") {
		t.Errorf("rejected item: %+v", results[1])
	}
	if got := read(t, fetch.RejectedDir(dest), "Loose Book", "readme.txt"); got != "no audio here" {
		t.Errorf("rejected files are kept aside, got %q", got)
	}
	for _, want := range []string{"ignored 1 line(s) that are not magnet links", "check: spoken language and edition: ffmpeg is not installed",
		"1 download(s) were rejected by the check and moved to"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("missing %q in:\n%s", want, stderr)
		}
	}
	if strings.Contains(stdout+stderr, "the-key") {
		t.Error("the key must never be printed")
	}

	// With the check off, nothing is examined; and a second run has nothing to do.
	stdout, _ = capture(t, func() { cmdDownload(append(args, "-check=false", "-quiet"), testConfig()) })
	results = nil
	_ = json.Unmarshal([]byte(stdout), &results)
	if len(box.added) != 2 || len(results) != 2 || results[0].Check != nil || results[1].Status != fetch.DownloadRejected {
		t.Errorf("second run: added %v, results %+v", box.added, results)
	}
}

func TestGetCommandFindsThenDownloads(t *testing.T) {
	home := sandbox(t)
	dest := filepath.Join(home, "library")
	list := write(t, filepath.Join(home, "books.txt"),
		"Frankenstein | Mary Shelley\nNo Such Book | Nobody Atall\n"+magnet(hashB)+"\n")
	keyFile := write(t, filepath.Join(home, "my.key"), "file-key\n")
	cfg := testConfig()
	cfg.To = dest
	args := []string{list, "-key-file", keyFile, "-poll", "1ms", "-delay", "0"}

	_, stderr := capture(t, func() { cmdGet(args, cfg) })
	if box.key != "file-key" {
		t.Errorf("the key comes from -key-file, got %q", box.key)
	}
	if got := read(t, dest, "Frankenstein", "01.mp3"); got != "frank-1" {
		t.Errorf("the book that was found is downloaded, got %q", got)
	}
	if got := read(t, dest, "Dracula", "01.mp3"); got != "drac-1" {
		t.Errorf("a magnet link in the list skips the search, got %q", got)
	}
	for _, want := range []string{"downloading 2 item(s) to " + dest, "1 book(s) were not downloaded because no confident match was found"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("missing %q in:\n%s", want, stderr)
		}
	}

	// The report lives in the folder and says what became of each book.
	rows := report(t, fetch.BookkeepingDir(dest))
	if len(rows) != 2 || rows[0]["download"] != fetch.DownloadDone || rows[1]["status"] != fetch.StatusNotFound || rows[1]["download"] != "" {
		t.Errorf("report: %v", rows)
	}

	// Running the same command again searches for nothing it already matched
	// and adds nothing to the service.
	site.searches = 0
	capture(t, func() { cmdGet(args, cfg) })
	if len(box.added) != 2 {
		t.Errorf("a second run must not add links again: %v", box.added)
	}
}

func TestGetFallsBackWhenTheCheckRejectsTheFirstChoice(t *testing.T) {
	home := sandbox(t)
	dest := filepath.Join(home, "library")
	// Two equally good listings; the first one turns out to hold no audio.
	site.listings = []fetch.Candidate{
		listing(hashC, "Frankenstein - Mary Shelley"),
		listing(hashA, "Frankenstein - Mary Shelley"),
	}
	site.listings[0].Bitrate = "128 Kbps" // the better-sounding listing is chosen first
	list := write(t, filepath.Join(home, "books.txt"), "Frankenstein | Mary Shelley\n")
	t.Setenv("FAKEBOX_API_KEY", "k")

	_, stderr := capture(t, func() { cmdGet([]string{list, dest, "-poll", "1ms", "-delay", "0"}, testConfig()) })
	if strings.Join(box.added, ",") != hashC+","+hashA {
		t.Fatalf("the rejected first choice should be followed by the next-best listing, added %v\n%s", box.added, stderr)
	}
	if !strings.Contains(stderr, "trying the next-best listing") {
		t.Errorf("it should say it is moving on:\n%s", stderr)
	}
	if got := read(t, dest, "Frankenstein", "01.mp3"); got != "frank-1" {
		t.Errorf("the fallback is what ends up in the library, got %q", got)
	}
	rows := report(t, fetch.BookkeepingDir(dest))
	if len(rows) != 1 || rows[0]["download"] != fetch.DownloadDone || !strings.Contains(rows[0]["note"], "the first choice failed the check") {
		t.Errorf("the report should record the fallback: %v", rows)
	}
}

func TestNextBestLinks(t *testing.T) {
	sandbox(t)
	alternate := func(hash string) fetch.Alternate {
		c := listing(hash, "Frankenstein - Mary Shelley")
		c.Source = "FakeSite"
		return fetch.Alternate{Score: 0.9, Candidate: &c, Magnet: magnet(hash)}
	}
	first := listing(hashA, "Frankenstein - Mary Shelley")
	first.Source = "FakeSite"
	fine := listing(hashB, "Dracula - Bram Stoker")
	results := []fetch.Result{
		{Wanted: fetch.Wanted{Title: "Frankenstein", Author: "Mary Shelley", Raw: "Frankenstein | Mary Shelley"},
			Status: fetch.StatusMatched, Candidate: &first, Magnet: magnet(hashA), Alternates: []fetch.Alternate{alternate(hashC)}},
		{Wanted: fetch.Wanted{Raw: "Dracula"}, Status: fetch.StatusMatched, Candidate: &fine, Magnet: magnet(hashB)},
	}
	downloads := map[string]fetch.DownloadResult{hashA: {Status: fetch.DownloadNoSeeds}, hashB: {Status: fetch.DownloadDone}}
	tried := map[string]bool{hashA: true, hashB: true}
	searchedAgain := map[string]bool{}
	search := &searchFlags{opts: fetch.DefaultOptions(), sources: []fetch.Source{site, other}}

	// A dead first choice moves on to its alternate; a book that downloaded is left alone.
	links := nextBestLinks(results, downloads, tried, searchedAgain, search)
	if len(links) != 1 || links[0].Hash != hashC || results[0].Candidate.InfoHash != hashC ||
		!strings.Contains(results[0].Note, "the first choice had no seeds") {
		t.Fatalf("links %v, result %+v", links, results[0])
	}

	// When the alternate is dead too and none are left, the other sources
	// are asked once.
	const hashD = "dddddddddddddddddddddddddddddddddddddddd"
	other.listings = []fetch.Candidate{listing(hashD, "Frankenstein - Mary Shelley")}
	downloads[hashC] = fetch.DownloadResult{Status: fetch.DownloadRejected}
	links = nextBestLinks(results, downloads, tried, searchedAgain, search)
	if len(links) != 1 || links[0].Hash != hashD || results[0].Candidate.Source != "OtherSite" ||
		!strings.Contains(results[0].Note, "found elsewhere: no usable listing on FakeSite") {
		t.Fatalf("links %v, result %+v", links, results[0])
	}

	// And if that fails as well, the book is given up on rather than searched for forever.
	downloads[hashD] = fetch.DownloadResult{Status: fetch.DownloadNoSeeds}
	searches := other.searches
	if links = nextBestLinks(results, downloads, tried, searchedAgain, search); len(links) != 0 || other.searches != searches {
		t.Errorf("nothing left to try: links %v, %d further searches", links, other.searches-searches)
	}
}

func TestCheckCommand(t *testing.T) {
	home := sandbox(t)
	library := filepath.Join(home, "library")
	write(t, filepath.Join(library, "Good Book", "01.mp3"), "x")
	write(t, filepath.Join(library, "Good Book", "disc 2", "02.mp3"), "x")
	write(t, filepath.Join(library, "Good Book", ".hidden.mp3"), "x")
	write(t, filepath.Join(library, "Radio Drama Book", "01.mp3"), "x")
	write(t, filepath.Join(library, ".audiobook-fetch", "state.json"), "{}")
	write(t, filepath.Join(library, "loose-file.mp3"), "x")
	cfg := testConfig()
	cfg.To = library

	stdout, stderr := capture(t, func() { cmdCheck([]string{"-json", "-lang", "any"}, cfg) })
	var verdicts map[string]fetch.Verdict
	if err := json.Unmarshal([]byte(stdout), &verdicts); err != nil {
		t.Fatalf("-json should print the verdicts on stdout: %v\n%s", err, stdout)
	}
	if len(verdicts) != 2 {
		t.Fatalf("each subfolder is one book; hidden folders and loose files are not: %v", verdicts)
	}
	good := verdicts["Good Book"]
	if good.Status != fetch.VerdictOK || good.AudioFiles != 2 || good.Hours != 2 || good.Kbps != 64 {
		t.Errorf("good book: %+v", good)
	}
	if drama := verdicts["Radio Drama Book"]; drama.Status != fetch.VerdictFlagged || !strings.Contains(strings.Join(drama.Flags, " "), "radio drama") {
		t.Errorf("a telling folder name is flagged: %+v", drama)
	}
	for _, want := range []string{"check: spoken language and edition: ffmpeg is not installed", "2 books: 1 ok, 1 flagged, 0 rejected, 0 unchecked"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("missing %q in:\n%s", want, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(library, "Radio Drama Book", "01.mp3")); err != nil {
		t.Errorf("check must move nothing: %v", err)
	}
}

// zipOf returns the bytes of a zip holding the given files.
func zipOf(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = entry.Write([]byte(content))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func exists(parts ...string) bool {
	_, err := os.Stat(filepath.Join(parts...))
	return err == nil
}

func TestTidyCommand(t *testing.T) {
	home := sandbox(t)
	library := filepath.Join(home, "library")
	// One book still in its zip; one held twice, as an M4B and as two MP3s
	// of the same total length; one that is fine.
	write(t, filepath.Join(library, "Zipped Book", "Zipped Book.zip"), zipOf(t, map[string]string{"Zipped Book/01.mp3": "one"}))
	write(t, filepath.Join(library, "Twice", "Twice.m4b"), "x")
	write(t, filepath.Join(library, "Twice", "MP3", "01.mp3"), "x")
	write(t, filepath.Join(library, "Twice", "MP3", "02.mp3"), "x")
	write(t, filepath.Join(library, "Twice", "cover.jpg"), "x")
	write(t, filepath.Join(library, "Fine", "01.mp3"), "x")
	cfg := testConfig()
	cfg.To = library

	// A dry run says what it would do and touches nothing.
	_, stderr := capture(t, func() { cmdTidy([]string{"-dry-run"}, cfg) })
	for _, want := range []string{"would be unpacked: Zipped Book.zip", "keeping m4b (1 file, 64 kbps); duplicates: mp3 (2 files, 64 kbps)",
		"2 duplicate files would be moved to", "3 books: 1 archive(s) would be unpacked, 2 duplicate file(s) would be moved aside"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("dry run: missing %q in:\n%s", want, stderr)
		}
	}
	if !exists(library, "Zipped Book", "Zipped Book.zip") || !exists(library, "Twice", "MP3", "01.mp3") || exists(library, ".audiobook-fetch") {
		t.Fatal("a dry run must change nothing")
	}

	// -book limits it to the folders named.
	_, stderr = capture(t, func() { cmdTidy([]string{"-book", "zipped"}, cfg) })
	if !strings.Contains(stderr, "unpacked Zipped Book.zip: 1 files") || !strings.Contains(stderr, "1 books: 1 archive(s) unpacked, 0 duplicate file(s) moved aside") {
		t.Errorf("-book:\n%s", stderr)
	}
	if exists(library, "Zipped Book", "Zipped Book.zip") || read(t, library, "Zipped Book", "Zipped Book", "01.mp3") != "one" {
		t.Error("the zip should be unpacked and deleted")
	}
	if !exists(library, "Twice", "MP3", "01.mp3") {
		t.Error("-book must leave the other books alone")
	}

	// The rest: the worse format is moved aside, not deleted.
	_, stderr = capture(t, func() { cmdTidy([]string{library}, cfg) })
	if !strings.Contains(stderr, "3 books: 0 archive(s) unpacked, 2 duplicate file(s) moved aside") {
		t.Errorf("tidy:\n%s", stderr)
	}
	if !exists(library, "Twice", "Twice.m4b") || !exists(library, "Twice", "cover.jpg") || exists(library, "Twice", "MP3") {
		t.Error("the M4B and the cover stay; the MP3 folder goes")
	}
	if !exists(fetch.DuplicatesDir(library), "Twice", "MP3", "02.mp3") || !exists(library, "Fine", "01.mp3") {
		t.Error("duplicates are kept aside, and a book with one format is untouched")
	}

	// Run again, there is nothing left to do.
	_, stderr = capture(t, func() { cmdTidy(nil, cfg) })
	if !strings.Contains(stderr, "3 books: 0 archive(s) unpacked, 0 duplicate file(s) moved aside") {
		t.Errorf("second run:\n%s", stderr)
	}

	// With both halves switched off it only counts.
	_, stderr = capture(t, func() { cmdTidy([]string{"-unpack=false", "-keep-one-format=false"}, cfg) })
	if !strings.Contains(stderr, "3 books: 0 archive(s) unpacked") {
		t.Errorf("switched off:\n%s", stderr)
	}
}

func TestDownloadUnpacksAndKeepsOneFormat(t *testing.T) {
	home := sandbox(t)
	dest := filepath.Join(home, "library")
	box.catalog[hashA] = map[string]string{
		"Frankenstein/Frankenstein.zip": zipOf(t, map[string]string{"Frankenstein.m4b": "whole book"}),
		"Frankenstein/01.mp3":           "frank-1",
		"Frankenstein/02.mp3":           "frank-2",
	}
	links := write(t, filepath.Join(home, "magnets.txt"), magnet(hashA)+"\n")
	t.Setenv("FAKEBOX_API_KEY", "k")

	_, stderr := capture(t, func() { cmdDownload([]string{links, dest, "-poll", "1ms"}, testConfig()) })
	for _, want := range []string{"unpacked Frankenstein.zip: 1 files", "keeping m4b (1 file, 64 kbps); duplicates: mp3 (2 files, 64 kbps)",
		"2 file(s) repeating a book in another format were moved to"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("missing %q in:\n%s", want, stderr)
		}
	}
	if !exists(dest, "Frankenstein", "Frankenstein.m4b") || exists(dest, "Frankenstein", "Frankenstein.zip") || exists(dest, "Frankenstein", "01.mp3") {
		t.Error("the library should hold the M4B alone")
	}
	if !exists(fetch.DuplicatesDir(dest), "Frankenstein", "01.mp3") {
		t.Error("the MP3s should be set aside")
	}

	// Both can be switched off, in which case the files stay as they came.
	other := filepath.Join(home, "untouched")
	capture(t, func() {
		cmdDownload([]string{links, other, "-poll", "1ms", "-unpack=false", "-keep-one-format=false"}, testConfig())
	})
	if !exists(other, "Frankenstein", "Frankenstein.zip") || !exists(other, "Frankenstein", "01.mp3") {
		t.Error("with both off nothing is unpacked or moved")
	}
}

func TestDoctorSaysWhatIsMissingAndHowToFixIt(t *testing.T) {
	home := sandbox(t)

	// A bare machine: no key, no config, only ffprobe.
	var out bytes.Buffer
	if cmdDoctor(nil, testConfig(), &out) {
		t.Error("without a key the setup is not ready")
	}
	for _, want := range []string{"MISS downloader key", "FAKEBOX_API_KEY", "ok   sources            fakesite", "--   config file",
		"--   download folder    not set", "ok   ffprobe", "--   ffmpeg", "--   whisper model", "--   rar and 7z", "Not ready"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}

	// With a key the service accepts, a folder and a config, it is ready,
	// and the key itself is never shown.
	t.Setenv("FAKEBOX_API_KEY", "the-secret-key")
	write(t, filepath.Join(home, ".config", fetch.AppName, "config.json"), "{}")
	cfg := testConfig()
	cfg.To = home
	out.Reset()
	if !cmdDoctor(nil, cfg, &out) {
		t.Errorf("should be ready:\n%s", out.String())
	}
	for _, want := range []string{"ok   downloader key     FakeBox accepts it (0 items in the account)", "ok   config file", "ok   download folder    " + home, "Ready."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "the-secret-key") {
		t.Error("the key must never be printed")
	}

	// -offline finds the key but does not try it; a bad source name or a
	// folder that is not there is said plainly.
	cfg.Sources, cfg.To = []string{"fakesite", "nosuchsite"}, filepath.Join(home, "gone")
	out.Reset()
	if cmdDoctor([]string{"-offline"}, cfg, &out) {
		t.Error("an unknown source is not ready")
	}
	for _, want := range []string{"not tried: -offline", "MISS sources            config names nosuchsite", "cannot be reached right now"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}

func TestVersionString(t *testing.T) {
	if got := versionString(); got == "" {
		t.Error("a build always has something to call itself")
	}
	version = "v9.9.9"
	defer func() { version = "" }()
	if got := versionString(); got != "v9.9.9" {
		t.Errorf("a release build reports its tag, got %q", got)
	}
}

func TestArchiveLinksAreFetchedDirectlyWithNoKey(t *testing.T) {
	home := sandbox(t)
	const archiveHash = "64b9bedd5e330c548cd2b8f1c9dbc281b0bcda24"
	// A stand-in for archive.org: hash lookup, item metadata, and the files.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/advancedsearch.php":
			_, _ = w.Write([]byte(`{"response":{"docs":[{"identifier":"some_item"}]}}`))
		case "/metadata/some_item":
			_, _ = w.Write([]byte(`{"metadata":{"title":"Free Book","creator":"A. Writer"},"files":[
				{"name":"01.mp3","source":"original","format":"VBR MP3","size":"5"},
				{"name":"01_64kb.mp3","source":"derivative","format":"64Kbps MP3","size":"3"}]}`))
		case "/download/some_item/01.mp3":
			_, _ = w.Write([]byte("audio"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("ARCHIVE_URL", server.URL)

	dest := filepath.Join(home, "library")
	theirs := fetch.BuildMagnet(archiveHash, "Free Book", []string{"http://bt1.archive.org:6969/announce"})
	links := write(t, filepath.Join(home, "magnets.txt"), theirs+"\n"+magnet(hashA)+"\n")

	// No key at all: the Archive's book still arrives, and the link that
	// needs the service is reported, not silently dropped.
	var results []fetch.DownloadResult
	var err error
	_, stderr := capture(t, func() {
		results, err = downloadLinks(mustMagnets(t, read(t, links)), flagsFor(t, dest), false)
	})
	if err == nil || !strings.Contains(err.Error(), "no API key for fakebox") {
		t.Errorf("the service's link needs a key: %v", err)
	}
	if len(results) != 1 || results[0].Hash != archiveHash || results[0].Status != fetch.DownloadDone {
		t.Fatalf("results: %+v\n%s", results, stderr)
	}
	if got := read(t, dest, "Free Book - A. Writer", "01.mp3"); got != "audio" {
		t.Errorf("the original is fetched, got %q", got)
	}
	if exists(dest, "Free Book - A. Writer", "01_64kb.mp3") {
		t.Error("the re-encoded copy should not be fetched")
	}
	if len(box.added) != 0 {
		t.Errorf("nothing may be sent to the service without a key: %v", box.added)
	}

	// With a key, each link goes to the downloader that can fetch it.
	t.Setenv("FAKEBOX_API_KEY", "k")
	capture(t, func() { results, err = downloadLinks(mustMagnets(t, read(t, links)), flagsFor(t, dest), true) })
	if err != nil || len(results) != 2 || strings.Join(box.added, ",") != hashA {
		t.Errorf("results %+v, err %v, sent to the service: %v", results, err, box.added)
	}
}

func mustMagnets(t *testing.T, text string) []fetch.Magnet {
	t.Helper()
	magnets, bad := fetch.ParseMagnets(text)
	if bad != 0 || len(magnets) == 0 {
		t.Fatalf("bad test links: %d bad, %d good", bad, len(magnets))
	}
	return magnets
}

// flagsFor gives the download options a command would have, for dest.
func flagsFor(t *testing.T, dest string) *downloadFlags {
	t.Helper()
	var d downloadFlags
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	d.register(flags, testConfig())
	if err := flags.Parse([]string{"-to", dest, "-poll", "1ms"}); err != nil {
		t.Fatal(err)
	}
	return &d
}
