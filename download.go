package audiobookfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Downloading: magnet links -> a Downloader -> files in a folder.
//
// The run can be repeated safely. Items already on the service are not
// added twice, finished files are skipped, and half-finished files continue
// from where they stopped.

const (
	DownloadDone      = "downloaded"
	DownloadNotCached = "skipped_not_cached"
	DownloadNotReady  = "not_ready"
	DownloadStalled   = "stalled"
	DownloadNoSeeds   = "no_seeds" // given up on: the service made no progress on it
	DownloadRejected  = "rejected" // downloaded, checked, and found not to be what was asked for
	DownloadFailed    = "failed"

	partSuffix         = ".part"
	fileAttempts       = 4
	illegalInFileNames = `<>:"|?*`
)

// DownloadOptions control one download run.
type DownloadOptions struct {
	Dest        string        // folder the files go into
	CachedOnly  bool          // don't add links the service would have to fetch first
	RemoveAfter bool          // remove an item from the service once all its files are here (only items this tool added)
	Redo        bool          // fetch links again even if an earlier run already completed them
	Parallel    int           // files downloaded at once
	Wait        time.Duration // how long to wait for the service to finish fetching
	Poll        time.Duration // how often to check on items the service is still fetching
	StallAfter  time.Duration // give up on a link the service makes no progress on for this long; 0 waits the full Wait
	Idle        time.Duration // give up on a connection that sends nothing for this long
	// Check, when set, examines each item's files once they are all here.
	// An item it rejects is moved out of the folder (see RejectedDir) and
	// reported as rejected instead of downloaded.
	Check   func(paths []string) Verdict
	Log     func(format string, args ...any)
	OnBytes func(n int64) // called as data arrives, for progress display

	sleep func(time.Duration)
	now   func() time.Time
}

// DefaultDownloadOptions are the settings the CLI starts from.
func DefaultDownloadOptions() DownloadOptions {
	return DownloadOptions{Parallel: 3, Wait: time.Hour, Poll: 20 * time.Second, Idle: 90 * time.Second,
		StallAfter: 5 * time.Minute}
}

// DownloadResult is the outcome for one magnet link.
type DownloadResult struct {
	Hash    string `json:"hash"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Files   int    `json:"files"`   // files now complete on disk
	Fetched int    `json:"fetched"` // of those, how many this run downloaded
	Bytes   int64  `json:"bytes"`   // bytes this run downloaded
	Size    int64  `json:"size"`    // total size of the item's files
	Removed bool   `json:"removed"` // removed from the service afterwards
	// Check is what examining the files found, when checking is on.
	Check *Verdict `json:"check,omitempty"`
	Note  string   `json:"note,omitempty"`
}

// BookkeepingDir is the folder the tool keeps its own files in, inside a
// download folder: the record of what it added to the service, and its reports.
func BookkeepingDir(dest string) string { return filepath.Join(dest, "."+AppName) }

// RejectedDir is where the files of rejected items are moved to, so that
// they are out of the library but nothing is destroyed on a machine's say-so.
func RejectedDir(dest string) string { return filepath.Join(BookkeepingDir(dest), "rejected") }

// state is what the tool remembers between runs, per download folder.
type state struct {
	// Added is what this tool put on the service, so that removing afterwards
	// can never touch anything that was already in the account.
	Added map[string]bool `json:"added"`
	// Done is what has been fully downloaded here, so that a later run does
	// not hand the link to the service again just to find nothing to do.
	Done map[string]bool `json:"done"`
	// Rejected is what was downloaded and then found wanting, and why, so
	// that a later run does not fetch the same thing just to reject it again.
	Rejected map[string]string `json:"rejected,omitempty"`
	path     string
}

func loadState(dest string, service Downloader) *state {
	name := strings.ToLower(strings.ReplaceAll(service.Name(), " ", "-")) + "-state.json"
	s := &state{path: filepath.Join(BookkeepingDir(dest), name)}
	if data, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(data, s)
	}
	if s.Added == nil {
		s.Added = map[string]bool{}
	}
	if s.Done == nil {
		s.Done = map[string]bool{}
	}
	if s.Rejected == nil {
		s.Rejected = map[string]string{}
	}
	return s
}

func (s *state) save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Download sends every magnet to the service and brings the files home.
func Download(service Downloader, magnets []Magnet, opts DownloadOptions) ([]DownloadResult, error) {
	if opts.Log == nil {
		opts.Log = func(string, ...any) {}
	}
	if opts.OnBytes == nil {
		opts.OnBytes = func(int64) {}
	}
	if opts.sleep == nil {
		opts.sleep = time.Sleep
	}
	if opts.now == nil {
		opts.now = time.Now
	}
	opts.Parallel = max(1, opts.Parallel)
	if err := os.MkdirAll(BookkeepingDir(opts.Dest), 0o755); err != nil {
		return nil, err
	}
	st := loadState(opts.Dest, service)
	name := service.Name()

	account, err := service.List()
	if err != nil {
		return nil, err
	}
	present := indexByHash(account)

	var missing []string
	for _, m := range magnets {
		if _, ok := present[m.Hash]; !ok {
			missing = append(missing, m.Hash)
		}
	}
	cached := map[string]bool{}
	if len(missing) > 0 {
		if cached, err = service.CheckCached(missing); err != nil {
			return nil, err
		}
	}

	// Links waiting in the service's queue are held already, though List
	// does not show them; adding them again would only be refused.
	inQueue := map[string]bool{}
	if queuer, ok := service.(Queuer); ok && len(missing) > 0 {
		waiting, err := queuer.Queued()
		if err != nil {
			return nil, err
		}
		for _, hash := range waiting {
			inQueue[strings.ToLower(hash)] = true
		}
	}

	results := make([]DownloadResult, len(magnets))
	pending := map[int]bool{}
	addedAny := false
	for i, m := range magnets {
		results[i] = DownloadResult{Hash: m.Hash}
		if _, ok := present[m.Hash]; ok || inQueue[m.Hash] {
			pending[i] = true
			continue
		}
		if st.Done[m.Hash] && !opts.Redo {
			results[i].Status, results[i].Note = DownloadDone, "downloaded by an earlier run"
			opts.Log("[%d/%d] %s  downloaded by an earlier run", i+1, len(magnets), short(m.Hash))
			continue
		}
		if why, rejected := st.Rejected[m.Hash]; rejected && !opts.Redo {
			results[i].Status, results[i].Note = DownloadRejected, "rejected by an earlier run: "+why
			opts.Log("[%d/%d] %s  rejected by an earlier run: %s", i+1, len(magnets), short(m.Hash), why)
			continue
		}
		if opts.CachedOnly && !cached[m.Hash] {
			results[i].Status, results[i].Note = DownloadNotCached, name+" does not have it cached"
			opts.Log("[%d/%d] %s  skipped: not cached on %s", i+1, len(magnets), short(m.Hash), name)
			continue
		}
		if err := service.Add(m.URI); err != nil {
			results[i].Status, results[i].Note = DownloadFailed, "could not add to "+name+": "+err.Error()
			opts.Log("[%d/%d] %s  could not add: %v", i+1, len(magnets), short(m.Hash), err)
			continue
		}
		st.Added[m.Hash] = true
		if err := st.save(); err != nil {
			return nil, err
		}
		pending[i] = true
		addedAny = true
	}

	// The listing taken above predates anything just added, so take a new
	// one first in that case: cached links are ready the moment they are
	// added, and should not sit through a whole poll interval.
	deadline := opts.now().Add(opts.Wait)
	watch := map[string]progressMark{}
	for refresh := addedAny; len(pending) > 0; refresh = true {
		if refresh {
			if account, err = service.List(); err != nil {
				return results, err
			}
			present = indexByHash(account)
		}
		for i, m := range magnets {
			if !pending[i] {
				continue
			}
			item, ok := present[m.Hash]
			if !ok || !item.Ready || len(item.Files) == 0 {
				continue
			}
			delete(pending, i)
			results[i] = fetchItem(service, item, st, opts)
			r := results[i]
			opts.Log("[%d/%d] %s  %s: %d files, %s%s  %s", i+1, len(magnets), short(m.Hash), r.Status,
				r.Files, humanBytes(r.Size), removedNote(r, name), r.Name)
			if r.Check != nil {
				opts.Log("        check: %s", r.Check.Summary())
			}
		}

		// A link nobody is seeding never finishes, and meanwhile holds one
		// of the account's few active slots. Give up on anything that has
		// not moved for StallAfter, so the links behind it get their turn.
		// The service's first report cannot be trusted for this (everything
		// has no seeds in its first minute), hence the patience.
		for i, m := range magnets {
			item, held := present[m.Hash]
			if opts.StallAfter <= 0 || !pending[i] || !held {
				delete(watch, m.Hash)
				continue
			}
			mark, watched := watch[m.Hash]
			if !watched || item.Progress > mark.progress {
				watch[m.Hash] = progressMark{item.Progress, opts.now()}
				continue
			}
			if item.Progress >= 1 || opts.now().Sub(mark.since) < opts.StallAfter {
				continue
			}
			delete(pending, i)
			results[i] = giveUp(service, item, st, opts)
			opts.Log("[%d/%d] %s  no seeds: %s", i+1, len(magnets), short(m.Hash), results[i].Note)
		}
		if len(pending) == 0 {
			break
		}

		// Finished items have just been collected (and perhaps removed), so
		// this is the moment the account has room to start queued links.
		queued := map[string]bool{}
		if queuer, ok := service.(Queuer); ok {
			var hashes []string
			for i, m := range magnets {
				if pending[i] {
					hashes = append(hashes, m.Hash)
				}
			}
			waiting, err := queuer.AdvanceQueue(hashes)
			if err != nil {
				opts.Log("could not check %s's queue: %v", name, err)
			}
			for _, hash := range waiting {
				queued[hash] = true
			}
		}
		fetching, stalled := 0, 0
		for i, m := range magnets {
			if item, ok := present[m.Hash]; pending[i] && ok && !queued[m.Hash] {
				if item.Stalled {
					stalled++
				} else {
					fetching++
				}
			}
		}

		if !opts.now().Before(deadline) {
			for i, m := range magnets {
				if !pending[i] {
					continue
				}
				item, held := present[m.Hash]
				results[i].Status = DownloadNotReady
				results[i].Name = item.Name
				switch {
				case queued[m.Hash]:
					results[i].Note = fmt.Sprintf("queued on %s behind the account's limit on active links; run again later", name)
				case held && item.Stalled:
					results[i].Status = DownloadStalled
					results[i].Note = fmt.Sprintf("%s cannot fetch it (%s) and it is holding one of the account's active slots; "+
						"remove it there to let queued links start", name, item.State)
				case held:
					results[i].Note = fmt.Sprintf("%s is still fetching it (%s, %.0f%%); run again later",
						name, item.State, item.Progress*100)
				default:
					results[i].Note = name + " has not started on it; run again later"
				}
				opts.Log("[%d/%d] %s  %s: %s", i+1, len(magnets), short(m.Hash), strings.ReplaceAll(results[i].Status, "_", " "), results[i].Note)
			}
			break
		}
		opts.Log("waiting on %s for %d item(s): %d being fetched, %d stalled, %d queued",
			name, len(pending), fetching, stalled, len(queued))
		opts.sleep(opts.Poll)
	}
	return results, nil
}

// progressMark is how far the service had got with a link, and since when.
type progressMark struct {
	progress float64
	since    time.Time
}

// giveUp stops waiting for a link the service is getting nowhere with, and
// takes it off the service if this tool put it there.
func giveUp(service Downloader, item Item, st *state, opts DownloadOptions) DownloadResult {
	hash, name := strings.ToLower(item.Hash), service.Name()
	result := DownloadResult{Hash: hash, Name: item.Name, Status: DownloadNoSeeds}
	result.Note = fmt.Sprintf("%s made no progress on it for %s (%s, %.0f%%)", name, opts.StallAfter, item.State, item.Progress*100)
	if !st.Added[hash] {
		return result // it was in the account before this tool came along
	}
	if err := service.Remove(item); err != nil {
		result.Note += "; could not remove it from " + name + ": " + err.Error()
		return result
	}
	result.Removed = true
	result.Note += "; removed from " + name
	delete(st.Added, hash)
	if err := st.save(); err != nil {
		result.Note += ", but could not update the state file: " + err.Error()
	}
	return result
}

func indexByHash(items []Item) map[string]Item {
	out := make(map[string]Item, len(items))
	for _, item := range items {
		out[strings.ToLower(item.Hash)] = item
	}
	return out
}

func short(hash string) string { return hash[:min(8, len(hash))] }

func removedNote(r DownloadResult, service string) string {
	if r.Removed {
		return ", removed from " + service
	}
	return ""
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// fetchItem downloads every file of one ready item, a few at a time.
func fetchItem(service Downloader, item Item, st *state, opts DownloadOptions) DownloadResult {
	hash := strings.ToLower(item.Hash)
	result := DownloadResult{Hash: hash, Name: item.Name}

	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		slots  = make(chan struct{}, opts.Parallel)
		errs   []string
		client = &http.Client{}
	)
	var paths []string
	for _, file := range item.Files {
		result.Size += file.Size
		path, err := localPath(opts.Dest, item, file)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		paths = append(paths, path)
		wg.Add(1)
		slots <- struct{}{}
		go func(file File, path string) {
			defer wg.Done()
			defer func() { <-slots }()
			written, fetched, err := fetchFile(service, client, item, file, path, opts)
			mu.Lock()
			defer mu.Unlock()
			result.Bytes += written
			if err != nil {
				errs = append(errs, err.Error())
				return
			}
			result.Files++
			if fetched {
				result.Fetched++
			}
		}(file, path)
	}
	wg.Wait()

	if len(errs) > 0 {
		result.Status = DownloadFailed
		result.Note = fmt.Sprintf("%d of %d files failed; first: %s", len(errs), len(item.Files), errs[0])
		return result
	}
	result.Status = DownloadDone
	st.Done[hash] = true
	delete(st.Rejected, hash) // a redo gets a fresh verdict
	if opts.Check != nil {
		verdict := opts.Check(paths)
		result.Check = &verdict
		if verdict.Status == VerdictRejected {
			why := strings.Join(verdict.Reasons, "; ")
			result.Status = DownloadRejected
			delete(st.Done, hash)
			st.Rejected[hash] = why
			if moved, err := setAside(opts.Dest, paths); err != nil {
				result.Note = why + "; could not move its files out of the folder: " + err.Error()
			} else {
				result.Note = why + "; files moved to " + moved
			}
		}
	}
	if err := st.save(); err != nil {
		result.Note = strings.TrimPrefix(result.Note+"; could not update the state file: "+err.Error(), "; ")
	}

	name := service.Name()
	switch {
	case result.Status == DownloadRejected && st.Added[hash]:
		// Done with it either way; do not leave it holding a place.
		if err := service.Remove(item); err == nil {
			result.Removed = true
			delete(st.Added, hash)
			_ = st.save()
		}
	case result.Status == DownloadRejected:
	case !opts.RemoveAfter:
	case !st.Added[hash]:
		result.Note = "left on " + name + ": it was already in your account"
	default:
		if err := service.Remove(item); err != nil {
			result.Note = "downloaded, but could not remove from " + name + ": " + err.Error()
			break
		}
		result.Removed = true
		delete(st.Added, hash)
		if err := st.save(); err != nil {
			result.Note = "removed from " + name + ", but could not update the state file: " + err.Error()
		}
	}
	return result
}

// setAside moves an item's files from the download folder to RejectedDir,
// keeping their layout, and tidies away the folders that leaves empty.
func setAside(dest string, paths []string) (string, error) {
	aside := RejectedDir(dest)
	for _, path := range paths {
		rel, err := filepath.Rel(dest, path)
		if err != nil {
			return aside, err
		}
		target := filepath.Join(aside, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return aside, err
		}
		if err := os.Rename(path, target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return aside, err
		}
		// Remove now-empty folders, innermost first; Remove refuses a folder
		// that still holds something, which is exactly the stopping rule.
		for dir := filepath.Dir(path); dir != dest && strings.HasPrefix(dir, dest); dir = filepath.Dir(dir) {
			if os.Remove(dir) != nil {
				break
			}
		}
	}
	return aside, nil
}

// localPath maps a file to a path under dest, refusing anything that would
// land outside it and replacing characters that network shares reject.
func localPath(dest string, item Item, file File) (string, error) {
	var parts []string
	for _, part := range strings.FieldsFunc(file.Path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part = cleanSegment(part); part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("file %s has no usable name", file.ID)
	}
	if len(parts) == 1 { // a bare file: keep it in a folder named after its item
		folder := cleanSegment(item.Name)
		if folder == "" {
			folder = short(strings.ToLower(item.Hash))
		}
		parts = append([]string{folder}, parts...)
	}
	if strings.EqualFold(parts[0], filepath.Base(BookkeepingDir(dest))) {
		return "", fmt.Errorf("file %s would be written into the tool's own folder", file.ID)
	}
	path := filepath.Join(append([]string{dest}, parts...)...)
	if rel, err := filepath.Rel(dest, path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("file %s would be written outside the destination", file.ID)
	}
	return path, nil
}

func cleanSegment(segment string) string {
	segment = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(illegalInFileNames, r) {
			return '_'
		}
		return r
	}, segment)
	segment = strings.TrimRight(strings.TrimSpace(segment), ". ")
	if segment == "." || segment == ".." {
		return ""
	}
	return segment
}

var urlQuery = regexp.MustCompile(`\?[^\s"']+`)

// withoutSecrets strips query strings from any URL in an error. Download
// links carry tokens there, and errors end up in logs and reports.
func withoutSecrets(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(urlQuery.ReplaceAllString(err.Error(), "?…"))
}

// fetchFile gets one file onto disk. fetched is false when it was already
// complete. A fresh link is requested for every attempt.
func fetchFile(service Downloader, client *http.Client, item Item, file File, path string, opts DownloadOptions) (written int64, fetched bool, err error) {
	if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() && info.Size() == file.Size {
		return 0, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, false, err
	}
	for attempt := 1; attempt <= fileAttempts; attempt++ {
		var link string
		if link, err = service.Link(item, file); err == nil {
			var n int64
			n, err = fetchTo(client, link, path+partSuffix, file.Size, opts.Idle, opts.OnBytes)
			written += n
			if err == nil {
				if err = os.Rename(path+partSuffix, path); err == nil {
					return written, true, nil
				}
			}
		}
		if attempt < fileAttempts {
			opts.sleep(time.Duration(attempt) * 5 * time.Second)
		}
	}
	return written, false, fmt.Errorf("%s: %w", filepath.Base(path), withoutSecrets(err))
}

// fetchTo downloads link into part, continuing from whatever part already
// holds, and checks the result is exactly want bytes long.
func fetchTo(client *http.Client, link, part string, want int64, idle time.Duration, onBytes func(int64)) (written int64, err error) {
	var have int64
	if info, statErr := os.Stat(part); statErr == nil {
		have = info.Size()
	}
	if have > want {
		have = 0 // not a prefix of this file; start over
	}
	if have == want {
		if have == 0 {
			return 0, os.WriteFile(part, nil, 0o644)
		}
		return 0, nil
	}

	// One stalled connection must not hang the whole run.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if idle <= 0 {
		idle = 90 * time.Second
	}
	watchdog := time.AfterFunc(idle, cancel)
	defer watchdog.Stop()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return 0, err
	}
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	flags := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case http.StatusPartialContent:
		flags |= os.O_APPEND
	case http.StatusOK: // server ignored the range: take it from the top
		flags |= os.O_TRUNC
		have = 0
	default:
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	out, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return 0, err
	}

	buf := make([]byte, 256<<10)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			watchdog.Reset(idle)
			if _, writeErr := out.Write(buf[:n]); writeErr != nil {
				out.Close()
				return written, writeErr
			}
			written += int64(n)
			onBytes(int64(n))
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			out.Close()
			if errors.Is(readErr, context.Canceled) {
				readErr = fmt.Errorf("connection stalled for %s", idle)
			}
			return written, readErr
		}
	}
	if err := out.Close(); err != nil {
		return written, err
	}
	if got := have + written; got != want {
		return written, fmt.Errorf("got %d bytes, expected %d", got, want)
	}
	return written, nil
}
