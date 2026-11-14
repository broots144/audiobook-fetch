package audiobookfetch

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hashC = "cccccccccccccccccccccccccccccccccccccccc"
	hashZ = "ffffffffffffffffffffffffffffffffffffffff"

	linkToken = "secret-link-token"
)

func magnetFor(hash string) Magnet {
	return Magnet{Hash: hash, URI: "magnet:?xt=urn:btih:" + hash + "&dn=x"}
}

// fakeService is an in-memory Downloader, with a real HTTP server for the
// file downloads so resuming and failures can be exercised.
type fakeService struct {
	mu    sync.Mutex
	files *httptest.Server

	items       map[string]*Item  // in the account, by hash
	catalog     map[string][]File // what a hash contains once the service has it
	cached      map[string]bool   // hashes the service can serve immediately
	readyAfter  map[string]int    // List calls before a fetched item is ready; -1 = never
	content     map[string][]byte // file bytes by file id
	broken      map[string]bool   // files whose download always fails
	ignoreRange bool

	nextID  int
	added   []string
	removed []string
	ranges  []string
}

func newFakeService(t *testing.T) *fakeService {
	f := &fakeService{items: map[string]*Item{}, catalog: map[string][]File{}, cached: map[string]bool{},
		readyAfter: map[string]int{}, content: map[string][]byte{}, broken: map[string]bool{}, nextID: 100}
	f.files = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := strings.TrimPrefix(r.URL.Path, "/file/")
		f.ranges = append(f.ranges, r.Header.Get("Range"))
		if f.broken[id] || r.URL.Query().Get("token") != linkToken {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		if f.ignoreRange {
			r.Header.Del("Range")
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.content[id]))
	}))
	t.Cleanup(f.files.Close)
	return f
}

func (f *fakeService) id() string {
	f.nextID++
	return fmt.Sprint(f.nextID)
}

// has registers what a hash contains; paths map to file contents.
func (f *fakeService) has(hash string, cached bool, files map[string]string) {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		id := f.id()
		f.content[id] = []byte(files[path])
		f.catalog[hash] = append(f.catalog[hash], File{ID: id, Path: path, Size: int64(len(files[path]))})
	}
	f.cached[hash] = cached
}

// inAccount puts an item in the account as if it had been added elsewhere.
func (f *fakeService) inAccount(hash string) *Item {
	item := &Item{ID: f.id(), Hash: hash, Name: "item-" + hash[:4], State: "cached", Ready: true, Files: f.catalog[hash]}
	f.items[hash] = item
	return item
}

func (f *fakeService) Name() string { return "Fake" }

func (f *fakeService) List() ([]Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var list []Item
	for hash, item := range f.items {
		if left := f.readyAfter[hash]; left > 0 {
			f.readyAfter[hash] = left - 1
		} else if left == 0 && !item.Ready {
			item.Ready, item.Progress, item.State, item.Files = true, 1, "completed", f.catalog[hash]
		}
		list = append(list, *item)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list, nil
}

func (f *fakeService) CheckCached(hashes []string) (map[string]bool, error) {
	found := map[string]bool{}
	for _, hash := range hashes {
		if f.cached[hash] {
			found[hash] = true
		}
	}
	return found, nil
}

func (f *fakeService) Add(magnet string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	hash := NormalizeHash(btih.FindStringSubmatch(magnet)[1])
	f.added = append(f.added, hash)
	item := &Item{ID: f.id(), Hash: hash, Name: "item-" + hash[:4], State: "downloading", Progress: 0.25}
	if f.cached[hash] {
		item.Ready, item.State, item.Files = true, "cached", f.catalog[hash]
	}
	f.items[hash] = item
	return nil
}

func (f *fakeService) Link(item Item, file File) (string, error) {
	return f.files.URL + "/file/" + file.ID + "?token=" + linkToken, nil
}

func (f *fakeService) Remove(item Item) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for hash, held := range f.items {
		if held.ID == item.ID {
			delete(f.items, hash)
			f.removed = append(f.removed, item.ID)
		}
	}
	return nil
}

func testOptions(t *testing.T) DownloadOptions {
	opts := DefaultDownloadOptions()
	opts.Dest = t.TempDir()
	clock := time.Unix(0, 0)
	opts.now = func() time.Time { return clock }
	opts.sleep = func(d time.Duration) { clock = clock.Add(d) }
	return opts
}

func readFile(t *testing.T, parts ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func partFiles(dir string) (found []string) {
	_ = filepath.WalkDir(dir, func(path string, _ fs.DirEntry, _ error) error {
		if strings.HasSuffix(path, partSuffix) {
			found = append(found, path)
		}
		return nil
	})
	return found
}

func TestDownloadFetchesFilesAndIsRepeatable(t *testing.T) {
	f := newFakeService(t)
	f.has(hashA, true, map[string]string{"Folder/Disc 1/one.mp3": "first file", "Folder/two.mp3": "second", "Folder/empty.txt": ""})
	f.has(hashB, true, map[string]string{"single.m4b": "just one file"})
	opts := testOptions(t)
	var received int64
	opts.OnBytes = func(n int64) { received += n }
	opts.Parallel = 1 // so the byte counter needs no lock
	magnets := []Magnet{magnetFor(hashA), magnetFor(hashB)}

	results, err := Download(f, magnets, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, opts.Dest, "Folder", "Disc 1", "one.mp3"); got != "first file" {
		t.Errorf("one.mp3 = %q", got)
	}
	if got := readFile(t, opts.Dest, "Folder", "two.mp3"); got != "second" {
		t.Errorf("two.mp3 = %q", got)
	}
	if got := readFile(t, opts.Dest, "Folder", "empty.txt"); got != "" {
		t.Errorf("empty.txt = %q", got)
	}
	// A bare file goes in a folder named after its item, not loose in dest.
	if got := readFile(t, opts.Dest, "item-bbbb", "single.m4b"); got != "just one file" {
		t.Errorf("single.m4b = %q", got)
	}
	a := results[0]
	if a.Status != DownloadDone || a.Files != 3 || a.Fetched != 3 || a.Bytes != 16 || a.Size != 16 || a.Removed {
		t.Errorf("first run, item A: %+v", a)
	}
	if received != 16+13 {
		t.Errorf("progress hook saw %d bytes, want 29", received)
	}
	if strings.Join(f.added, ",") != hashA+","+hashB {
		t.Errorf("added = %v", f.added)
	}
	if left := partFiles(opts.Dest); len(left) > 0 {
		t.Errorf("part files left behind: %v", left)
	}

	downloads := len(f.ranges)
	results, err = Download(f, magnets, opts)
	if err != nil {
		t.Fatal(err)
	}
	if a = results[0]; a.Status != DownloadDone || a.Files != 3 || a.Fetched != 0 || a.Bytes != 0 {
		t.Errorf("second run, item A: %+v", a)
	}
	if len(f.added) != 2 || len(f.ranges) != downloads {
		t.Errorf("second run should add nothing and download nothing: added %v, downloads %d -> %d", f.added, downloads, len(f.ranges))
	}
}

func TestDownloadResumesAPartialFile(t *testing.T) {
	for _, ignoreRange := range []bool{false, true} {
		f := newFakeService(t)
		f.ignoreRange = ignoreRange
		f.has(hashA, true, map[string]string{"Folder/big.bin": "0123456789"})
		opts := testOptions(t)
		part := filepath.Join(opts.Dest, "Folder", "big.bin"+partSuffix)
		_ = os.MkdirAll(filepath.Dir(part), 0o755)
		_ = os.WriteFile(part, []byte("0123"), 0o644)

		results, err := Download(f, []Magnet{magnetFor(hashA)}, opts)
		if err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, opts.Dest, "Folder", "big.bin"); got != "0123456789" {
			t.Errorf("ignoreRange=%v: content = %q", ignoreRange, got)
		}
		wantBytes := int64(6)
		if ignoreRange {
			wantBytes = 10 // server sent the whole file again; it must replace, not append
		}
		if results[0].Bytes != wantBytes || f.ranges[0] != "bytes=4-" {
			t.Errorf("ignoreRange=%v: fetched %d bytes with Range %q", ignoreRange, results[0].Bytes, f.ranges[0])
		}
		if left := partFiles(opts.Dest); len(left) > 0 {
			t.Errorf("ignoreRange=%v: part file still there", ignoreRange)
		}
	}
}

func TestRemoveAfterOnlyRemovesWhatThisToolAdded(t *testing.T) {
	f := newFakeService(t)
	f.has(hashA, true, map[string]string{"A/a.mp3": "aaa"})
	f.has(hashB, true, map[string]string{"B/b.mp3": "bbb"})
	f.has(hashZ, true, map[string]string{"Z/movie.mkv": "zzz"})
	mine := f.inAccount(hashA)      // already in the account, and also in our list
	unrelated := f.inAccount(hashZ) // already in the account, nothing to do with us
	opts := testOptions(t)
	opts.RemoveAfter = true

	results, err := Download(f, []Magnet{magnetFor(hashA), magnetFor(hashB)}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != DownloadDone || results[0].Removed || !strings.Contains(results[0].Note, "already in your account") {
		t.Errorf("pre-existing item: %+v", results[0])
	}
	if results[1].Status != DownloadDone || !results[1].Removed {
		t.Errorf("item this tool added: %+v", results[1])
	}
	if len(f.removed) != 1 || f.items[hashB] != nil {
		t.Errorf("removed = %v, want only the one this tool added", f.removed)
	}
	if f.items[hashA] == nil || f.items[hashA].ID != mine.ID || f.items[hashZ] == nil || f.items[hashZ].ID != unrelated.ID {
		t.Errorf("items that were already in the account must survive: %v", f.items)
	}
	if _, err := os.Stat(filepath.Join(opts.Dest, "Z")); !os.IsNotExist(err) {
		t.Error("an unrelated item was downloaded")
	}
}

func TestRemoveAfterRemembersWhatAnEarlierRunAdded(t *testing.T) {
	f := newFakeService(t)
	f.has(hashB, true, map[string]string{"B/b.mp3": "bbb"})
	opts := testOptions(t)

	if _, err := Download(f, []Magnet{magnetFor(hashB)}, opts); err != nil {
		t.Fatal(err)
	}
	if len(f.removed) != 0 || f.items[hashB] == nil {
		t.Fatalf("nothing should be removed without the option: %v", f.removed)
	}
	if _, err := os.Stat(filepath.Join(BookkeepingDir(opts.Dest), "fake-state.json")); err != nil {
		t.Fatalf("the record of what was added should be in the bookkeeping folder: %v", err)
	}
	opts.RemoveAfter = true
	results, err := Download(f, []Magnet{magnetFor(hashB)}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !results[0].Removed || results[0].Fetched != 0 || f.items[hashB] != nil {
		t.Errorf("second run should remove it without downloading again: %+v", results[0])
	}
}

func TestRemoveAfterKeepsAnItemWhoseDownloadFailed(t *testing.T) {
	f := newFakeService(t)
	f.has(hashA, true, map[string]string{"A/good.mp3": "fine", "A/bad.mp3": "never arrives"})
	f.broken[f.catalog[hashA][0].ID] = true // "A/bad.mp3" sorts first
	opts := testOptions(t)
	opts.RemoveAfter = true

	results, err := Download(f, []Magnet{magnetFor(hashA)}, opts)
	if err != nil {
		t.Fatal(err)
	}
	r := results[0]
	if r.Status != DownloadFailed || r.Removed || r.Files != 1 || len(f.removed) != 0 || f.items[hashA] == nil {
		t.Errorf("result %+v, removed %v", r, f.removed)
	}
	if !strings.Contains(r.Note, "bad.mp3") || !strings.Contains(r.Note, "500") {
		t.Errorf("the note should say which file failed and why: %s", r.Note)
	}
	if got := readFile(t, opts.Dest, "A", "good.mp3"); got != "fine" {
		t.Errorf("the good file should still be saved, got %q", got)
	}
}

func TestDownloadErrorsDoNotLeakLinkTokens(t *testing.T) {
	err := withoutSecrets(errors.New(`Get "https://cdn.example/f/1?token=` + linkToken + `&x=1": connection reset`))
	if strings.Contains(err.Error(), linkToken) || !strings.Contains(err.Error(), "cdn.example/f/1") || !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("got %q", err)
	}
}

func TestCachedOnlyDoesNotAddUncachedLinks(t *testing.T) {
	f := newFakeService(t)
	f.has(hashA, true, map[string]string{"A/a.mp3": "aaa"})
	f.has(hashB, false, map[string]string{"B/b.mp3": "bbb"})
	opts := testOptions(t)
	opts.CachedOnly = true

	results, err := Download(f, []Magnet{magnetFor(hashA), magnetFor(hashB)}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != DownloadDone || results[1].Status != DownloadNotCached || strings.Join(f.added, ",") != hashA {
		t.Errorf("statuses %s, %s; added %v", results[0].Status, results[1].Status, f.added)
	}
}

func TestWaitsForTheServiceThenStopsWaiting(t *testing.T) {
	f := newFakeService(t)
	f.has(hashA, false, map[string]string{"A/a.mp3": "aaa"})
	f.has(hashB, false, map[string]string{"B/b.mp3": "bbb"})
	f.has(hashC, true, map[string]string{"C/c.mp3": "ccc"})
	f.readyAfter[hashA] = 3  // the service finishes fetching it after a few checks
	f.readyAfter[hashB] = -1 // never finishes
	opts := testOptions(t)
	opts.Wait, opts.Poll = 10*time.Minute, time.Minute
	started := opts.now()

	results, err := Download(f, []Magnet{magnetFor(hashA), magnetFor(hashB), magnetFor(hashC)}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != DownloadDone || results[2].Status != DownloadDone {
		t.Errorf("A %s, C %s", results[0].Status, results[2].Status)
	}
	if results[1].Status != DownloadNotReady || !strings.Contains(results[1].Note, "25%") {
		t.Errorf("B: %+v", results[1])
	}
	if waited := opts.now().Sub(started); waited != 10*time.Minute {
		t.Errorf("waited %s, want exactly the 10 minute limit", waited)
	}
	if got := readFile(t, opts.Dest, "A", "a.mp3"); got != "aaa" {
		t.Errorf("a.mp3 = %q", got)
	}
}

func TestLocalPathStaysInsideDestination(t *testing.T) {
	dest := t.TempDir()
	item := Item{Hash: hashA, Name: "The Item"}
	cases := map[string]string{
		"Folder/Sub/file.mp3":     "Folder/Sub/file.mp3",
		"../../etc/passwd":        "etc/passwd",
		"/abs/olute.mp3":          "abs/olute.mp3",
		"a/../../b.mp3":           "a/b.mp3",
		`C:\Users\x\what?.mp3`:    "C_/Users/x/what_.mp3",
		"Folder/trailing dot./f.": "Folder/trailing dot/f",
		"loose.mp3":               "The Item/loose.mp3",
		"..":                      "",
	}
	for name, want := range cases {
		got, err := localPath(dest, item, File{ID: "1", Path: name})
		if want == "" {
			if err == nil {
				t.Errorf("%q: expected an error, got %s", name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", name, err)
			continue
		}
		if rel, _ := filepath.Rel(dest, got); filepath.ToSlash(rel) != want {
			t.Errorf("%q -> %q, want %q", name, filepath.ToSlash(rel), want)
		}
	}
}

func TestParseMagnets(t *testing.T) {
	text := strings.Join([]string{
		"# comment",
		"",
		"magnet:?xt=urn:btih:" + strings.ToUpper(hashA) + "&dn=Some%20Name&tr=udp%3A%2F%2Ft.example",
		"magnet:?dn=first&xt=urn:btih:" + hashB,
		"magnet:?xt=urn:btih:" + hashA + "&dn=duplicate",
		"magnet:?xt=urn:btih:VKVKVKVKVKVKVKVKVKVKVKVKVKVKVKVK", // base32 form of aaaa...
		"magnet:?xt=urn:btih:tooshort",
		"https://example.com/not-a-magnet",
	}, "\n")
	magnets, bad := ParseMagnets(text)
	if len(magnets) != 2 || magnets[0].Hash != hashA || magnets[1].Hash != hashB || bad != 2 {
		t.Errorf("magnets %+v, bad %d", magnets, bad)
	}
	if !strings.Contains(magnets[0].URI, "tr=udp") {
		t.Errorf("the full link should be kept: %s", magnets[0].URI)
	}
}

// queueingService is a fakeService whose account fetches only a few links
// at once; the rest wait in a queue that never moves unless it is told to.
type queueingService struct {
	*fakeService
	limit   int
	queue   []string // magnets waiting, oldest first
	started []string
}

func (q *queueingService) active() int {
	n := 0
	for _, item := range q.items {
		if !item.Ready {
			n++
		}
	}
	return n
}

func (q *queueingService) Add(magnet string) error {
	hash := NormalizeHash(btih.FindStringSubmatch(magnet)[1])
	q.mu.Lock()
	// A cached link is ready at once and takes no slot; only links the
	// service has to fetch count against the limit.
	full := !q.cached[hash] && q.active() >= q.limit
	if full {
		q.queue = append(q.queue, magnet)
		q.added = append(q.added, hash)
	}
	q.mu.Unlock()
	if full {
		return nil
	}
	return q.fakeService.Add(magnet)
}

func (q *queueingService) Queued() ([]string, error) {
	var hashes []string
	for _, magnet := range q.queue {
		hashes = append(hashes, NormalizeHash(btih.FindStringSubmatch(magnet)[1]))
	}
	return hashes, nil
}

func (q *queueingService) AdvanceQueue(hashes []string) ([]string, error) {
	wanted := map[string]bool{}
	for _, h := range hashes {
		wanted[h] = true
	}
	var waiting, left []string
	for _, magnet := range q.queue {
		hash := NormalizeHash(btih.FindStringSubmatch(magnet)[1])
		q.mu.Lock()
		room := q.active() < q.limit
		q.mu.Unlock()
		if !wanted[hash] || !room {
			left = append(left, magnet)
			if wanted[hash] {
				waiting = append(waiting, hash)
			}
			continue
		}
		added := len(q.added)
		_ = q.fakeService.Add(magnet)
		q.added = q.added[:added] // it was counted when first queued
		q.started = append(q.started, hash)
	}
	q.queue = left
	return waiting, nil
}

func TestQueuedLinksAreStartedAsSlotsFree(t *testing.T) {
	f := newFakeService(t)
	for _, hash := range []string{hashA, hashB, hashC} {
		f.has(hash, false, map[string]string{hash[:1] + "/file.mp3": hash[:3]})
		f.readyAfter[hash] = 2
	}
	q := &queueingService{fakeService: f, limit: 1}
	opts := testOptions(t)
	opts.Wait, opts.Poll = time.Hour, time.Minute
	var log []string
	opts.Log = func(format string, args ...any) { log = append(log, fmt.Sprintf(format, args...)) }

	results, err := Download(q, []Magnet{magnetFor(hashA), magnetFor(hashB), magnetFor(hashC)}, opts)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range results {
		if r.Status != DownloadDone {
			t.Errorf("link %d: %+v", i, r)
		}
	}
	if strings.Join(q.started, ",") != hashB+","+hashC || len(q.queue) != 0 {
		t.Errorf("queued links should be started in order as room appears: started %v, still queued %v", q.started, q.queue)
	}
	if !strings.Contains(strings.Join(log, "\n"), "1 being fetched, 0 stalled, 2 queued") {
		t.Errorf("the wait line should say what is holding things up:\n%s", strings.Join(log, "\n"))
	}
}

func TestStalledAndQueuedLinksAreReportedAsSuch(t *testing.T) {
	f := newFakeService(t)
	f.has(hashA, false, map[string]string{"A/a.mp3": "aaa"})
	f.has(hashB, false, map[string]string{"B/b.mp3": "bbb"})
	f.readyAfter[hashA] = -1
	q := &queueingService{fakeService: f, limit: 1}
	opts := testOptions(t)
	opts.Wait, opts.Poll = 5*time.Minute, time.Minute

	// The first link gets the only slot and then stalls, so the second can never start.
	if err := q.Add(magnetFor(hashA).URI); err != nil {
		t.Fatal(err)
	}
	f.items[hashA].Stalled, f.items[hashA].State = true, "stalled (no seeds)"
	f.added = nil

	results, err := Download(q, []Magnet{magnetFor(hashA), magnetFor(hashB)}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != DownloadStalled || !strings.Contains(results[0].Note, "stalled (no seeds)") || !strings.Contains(results[0].Note, "active slots") {
		t.Errorf("stalled link: %+v", results[0])
	}
	if results[1].Status != DownloadNotReady || !strings.Contains(results[1].Note, "queued on Fake") {
		t.Errorf("queued link: %+v", results[1])
	}
}

func TestALaterRunDoesNotHandFinishedOrQueuedLinksBackToTheService(t *testing.T) {
	f := newFakeService(t)
	f.has(hashA, true, map[string]string{"A/a.mp3": "aaa"})
	f.has(hashB, false, map[string]string{"B/b.mp3": "bbb"})
	f.has(hashC, false, map[string]string{"C/c.mp3": "ccc"})
	f.readyAfter[hashB], f.readyAfter[hashC] = -1, -1
	q := &queueingService{fakeService: f, limit: 1}
	opts := testOptions(t)
	opts.RemoveAfter, opts.Wait, opts.Poll = true, 2*time.Minute, time.Minute
	magnets := []Magnet{magnetFor(hashA), magnetFor(hashB), magnetFor(hashC)}

	// First run: A is downloaded and removed, B takes the only slot, C is queued.
	if _, err := Download(q, magnets, opts); err != nil {
		t.Fatal(err)
	}
	if len(f.added) != 3 || len(q.queue) != 1 {
		t.Fatalf("after the first run: added %v, queue %v", f.added, q.queue)
	}

	results, err := Download(q, magnets, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.added) != 3 {
		t.Errorf("the second run must add nothing: finished and queued links are already accounted for; added %v", f.added)
	}
	if results[0].Status != DownloadDone || !strings.Contains(results[0].Note, "earlier run") || results[0].Fetched != 0 {
		t.Errorf("finished link: %+v", results[0])
	}
	if results[2].Status != DownloadNotReady || !strings.Contains(results[2].Note, "queued") {
		t.Errorf("queued link: %+v", results[2])
	}

	// Asking for it again does hand it back.
	opts.Redo = true
	if results, err = Download(q, magnets[:1], opts); err != nil || len(f.added) != 4 || results[0].Status != DownloadDone {
		t.Errorf("with Redo: err %v, added %v, result %+v", err, f.added, results[0])
	}
}
