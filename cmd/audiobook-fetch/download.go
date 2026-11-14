package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	fetch "github.com/broots144/audiobook-fetch"
	"github.com/broots144/audiobook-fetch/downloaders/archive"
)

// downloadFlags are the options that shape the download half of the job.
type downloadFlags struct {
	opts    fetch.DownloadOptions
	name    string
	keyFile string
	check   bool   // examine the audio once it is downloaded
	unpack  bool   // open archives before checking
	oneOnly bool   // keep one format when a book arrives in several
	model   string // whisper.cpp model file for the listening checks
	lang    string // language the audio should be in; set by the command
}

func (d *downloadFlags) register(flags *flag.FlagSet, cfg config) {
	d.opts = fetch.DefaultDownloadOptions()
	d.opts.Dest = cfg.To
	flags.StringVar(&d.opts.Dest, "to", d.opts.Dest, "folder to download into (same as naming it after the list)")
	flags.StringVar(&d.name, "downloader", cfg.Downloader, "service that fetches the links ("+strings.Join(fetch.DownloaderNames(), ", ")+")")
	flags.BoolVar(&d.opts.RemoveAfter, "remove-after", cfg.RemoveAfter, "remove each item from the service once its files are here (only items this tool added; anything already in your account is left alone)")
	flags.BoolVar(&d.opts.Redo, "redo", false, "download links again even if an earlier run already completed them")
	flags.BoolVar(&d.opts.CachedOnly, "cached-only", false, "skip links the service would have to fetch first")
	flags.IntVar(&d.opts.Parallel, "parallel", d.opts.Parallel, "files to download at once")
	flags.DurationVar(&d.opts.Wait, "wait", d.opts.Wait, "how long to wait for the service to fetch links it does not have yet")
	flags.DurationVar(&d.opts.StallAfter, "stall-after", d.opts.StallAfter, "give up on a link the service makes no progress on for this long, freeing its slot (0 = keep waiting)")
	flags.DurationVar(&d.opts.Poll, "poll", d.opts.Poll, "how often to check on links the service is still fetching")
	flags.BoolVar(&d.check, "check", cfg.Check == nil || *cfg.Check, "check each download's audio: playable, bitrate, spoken language, abridged or dramatized (needs ffprobe; listening needs ffmpeg, whisper-cli and a model)")
	flags.BoolVar(&d.unpack, "unpack", cfg.Unpack == nil || *cfg.Unpack, "unpack zip, rar and 7z archives once downloaded, then delete them (rar and 7z need 7zz, 7z, unar or bsdtar)")
	flags.BoolVar(&d.oneOnly, "keep-one-format", cfg.KeepOneFormat == nil || *cfg.KeepOneFormat, "when a download holds the same book in several formats, keep the best and move the rest aside (needs -check)")
	flags.StringVar(&d.model, "whisper-model", defaultModel(cfg), "whisper.cpp model file used to listen to samples")
	flags.StringVar(&d.keyFile, "key-file", "", "file holding the service's API key (default: <name>.key in the config folder, or the <NAME>_API_KEY variable)")
}

// cmdDownload downloads magnet links that are already in hand.
func cmdDownload(args []string, cfg config) {
	var download downloadFlags
	flags := flag.NewFlagSet("download", flag.ExitOnError)
	download.register(flags, cfg)
	flags.StringVar(&download.lang, "lang", fetch.DefaultOptions().Language, `language the audio should be in, or "any"`)
	printJSON := flags.Bool("json", false, "print the per-link results as JSON on stdout")
	quiet := flags.Bool("quiet", false, "only print the summary")
	flags.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s download [options] LINKS [FOLDER]\n\nSends each magnet link in LINKS to the downloader and saves its files in FOLDER.\n\noptions:\n", fetch.AppName)
		flags.PrintDefaults()
	}
	positional := parseArgs(flags, args)
	if len(positional) < 1 || len(positional) > 2 {
		flags.Usage()
		os.Exit(2)
	}
	dest, err := destination(positional, 1, download.opts.Dest)
	if err != nil {
		fatal(err)
	}
	download.opts.Dest = dest
	text, err := readInput(positional[0])
	if err != nil {
		fatal(err)
	}
	magnets, bad := fetch.ParseMagnets(text)
	if len(magnets) == 0 {
		fatal(fmt.Errorf("no magnet links found in %s", positional[0]))
	}
	if bad > 0 {
		fmt.Fprintf(os.Stderr, "ignored %d line(s) that are not magnet links\n", bad)
	}
	results, err := downloadLinks(magnets, &download, *quiet)
	if err != nil {
		fatal(err)
	}
	if *printJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		_ = enc.Encode(results)
	}
	for _, r := range results {
		if r.Status == fetch.DownloadFailed {
			os.Exit(1)
		}
	}
}

// downloadLinks sends each link to the downloader that can fetch it and
// returns every result. The Internet Archive's links go straight to the
// Archive, which needs no account; the rest go to the chosen service. One
// downloader failing (no key, say) does not stop the other.
func downloadLinks(magnets []fetch.Magnet, d *downloadFlags, quiet bool) ([]fetch.DownloadResult, error) {
	var direct, rest []fetch.Magnet
	for _, m := range magnets {
		if d.name != "archive" && archive.Handles(m.URI) {
			direct = append(direct, m)
		} else {
			rest = append(rest, m)
		}
	}
	var results []fetch.DownloadResult
	var firstErr error
	for _, group := range []struct {
		name    string
		magnets []fetch.Magnet
	}{{"archive", direct}, {d.name, rest}} {
		if len(group.magnets) == 0 {
			continue
		}
		got, err := downloadWith(group.name, group.magnets, d, quiet)
		results = append(results, got...)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return results, firstErr
}

// downloadWith runs the download through one downloader, showing progress,
// and prints a summary.
func downloadWith(name string, magnets []fetch.Magnet, d *downloadFlags, quiet bool) ([]fetch.DownloadResult, error) {
	// A downloader with no account takes no key; one that needs a key says
	// so itself when it is given none.
	service, err := fetch.NewDownloader(name, "")
	if err != nil {
		key, keyErr := readKey(name, d.keyFile)
		if keyErr != nil {
			return nil, keyErr
		}
		if service, err = fetch.NewDownloader(name, key); err != nil {
			return nil, err
		}
	}
	opts := d.opts
	if d.unpack {
		opts.Unpack = fetch.NewUnpacker()
		for _, gap := range opts.Unpack.Missing() {
			fmt.Fprintf(os.Stderr, "unpack: %s\n", gap)
		}
	}
	opts.KeepOneFormat = d.oneOnly
	if d.check {
		checker := newChecker(d.lang, d.model)
		for _, gap := range checker.Missing() {
			fmt.Fprintf(os.Stderr, "check: %s\n", gap)
		}
		if checker.FFprobe != "" {
			opts.Check = checker.Check
		}
	}
	var received atomic.Int64
	if !quiet {
		opts.Log = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
		opts.OnBytes = func(n int64) { received.Add(n) }
		stop := showThroughput(&received)
		defer stop()
	}

	start := time.Now()
	results, err := fetch.Download(service, magnets, opts)
	if err != nil {
		return results, err
	}

	counts := map[string]int{}
	var files, fetched, removed, flagged, repeats int
	var bytes int64
	for _, r := range results {
		if r.Check != nil && r.Check.Status == fetch.VerdictFlagged {
			flagged++
		}
		if r.Check != nil && r.Status == fetch.DownloadDone && opts.KeepOneFormat {
			repeats += len(r.Check.Duplicates)
		}
		counts[r.Status]++
		files += r.Files
		fetched += r.Fetched
		bytes += r.Bytes
		if r.Removed {
			removed++
		}
	}
	fmt.Fprintf(os.Stderr, "\n%d links: %d downloaded, %d no seeds, %d not ready yet, %d stalled, %d skipped (not cached), %d failed\n",
		len(results), counts[fetch.DownloadDone], counts[fetch.DownloadNoSeeds], counts[fetch.DownloadNotReady],
		counts[fetch.DownloadStalled], counts[fetch.DownloadNotCached], counts[fetch.DownloadFailed])
	fmt.Fprintf(os.Stderr, "%d files complete in %s (%d fetched this run, %.2f GB, %s)\n",
		files, opts.Dest, fetched, float64(bytes)/1e9, time.Since(start).Round(time.Second))
	if opts.RemoveAfter {
		fmt.Fprintf(os.Stderr, "%d item(s) removed from %s\n", removed, service.Name())
	}
	if counts[fetch.DownloadRejected] > 0 {
		fmt.Fprintf(os.Stderr, "%d download(s) were rejected by the check and moved to %s\n",
			counts[fetch.DownloadRejected], fetch.RejectedDir(opts.Dest))
	}
	if repeats > 0 {
		fmt.Fprintf(os.Stderr, "%d file(s) repeating a book in another format were moved to %s\n", repeats, fetch.DuplicatesDir(opts.Dest))
	}
	if flagged > 0 {
		fmt.Fprintf(os.Stderr, "%d download(s) were kept but flagged by the check; see the \"check:\" lines above\n", flagged)
	}
	if counts[fetch.DownloadNoSeeds] > 0 {
		fmt.Fprintf(os.Stderr, "%d link(s) were given up on: %s made no progress on them for %s (usually nobody is seeding them)\n",
			counts[fetch.DownloadNoSeeds], service.Name(), opts.StallAfter)
	}
	if counts[fetch.DownloadStalled] > 0 {
		fmt.Fprintf(os.Stderr, "%d link(s) are stalled on %s with nothing to fetch them from, each holding an active slot;\n"+
			"remove them there if queued links are waiting\n", counts[fetch.DownloadStalled], service.Name())
	}
	if counts[fetch.DownloadNotReady] > 0 {
		fmt.Fprintf(os.Stderr, "run the same command again later to collect what %s is still fetching\n", service.Name())
	}
	return results, nil
}

// showThroughput prints a line every half minute while data is arriving, so
// a long download is visibly alive. The returned function stops it.
func showThroughput(received *atomic.Int64) (stop func()) {
	const every = 30 * time.Second
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		var last int64
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				now := received.Load()
				if now > last {
					fmt.Fprintf(os.Stderr, "  … %.2f GB so far, %.1f MB/s\n", float64(now)/1e9, float64(now-last)/1e6/every.Seconds())
				}
				last = now
			}
		}
	}()
	return func() { close(done) }
}

// newChecker sets up the post-download check for a wanted language as the
// -lang option spells it.
func newChecker(lang, model string) *fetch.Checker {
	if strings.EqualFold(lang, "any") {
		lang = ""
	}
	return fetch.NewChecker(lang, model)
}

// defaultModel finds the whisper.cpp model: the configured one, else
// WHISPER_MODEL, else the first model file in the config folder's models/.
func defaultModel(cfg config) string {
	if cfg.WhisperModel != "" {
		return cfg.WhisperModel
	}
	if model := os.Getenv("WHISPER_MODEL"); model != "" {
		return model
	}
	found, _ := filepath.Glob(filepath.Join(configDir(), "models", "ggml-*.bin"))
	if len(found) > 0 {
		return found[0]
	}
	return ""
}

// cmdCheck examines audiobooks that are already in a folder: each subfolder
// is taken as one book. It reports and changes nothing.
func cmdCheck(args []string, cfg config) {
	flags := flag.NewFlagSet("check", flag.ExitOnError)
	lang := flags.String("lang", fetch.DefaultOptions().Language, `language the audio should be in, or "any"`)
	model := flags.String("whisper-model", defaultModel(cfg), "whisper.cpp model file used to listen to samples")
	printJSON := flags.Bool("json", false, "print the verdicts as JSON on stdout")
	flags.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s check [options] [FOLDER]\n\nChecks the audio of every book in FOLDER (each subfolder is one book): playable,\nbitrate, spoken language, abridged or dramatized. Nothing is moved or deleted.\n\noptions:\n", fetch.AppName)
		flags.PrintDefaults()
	}
	positional := parseArgs(flags, args)
	if len(positional) > 1 {
		flags.Usage()
		os.Exit(2)
	}
	folder, err := destination(positional, 0, cfg.To)
	if err != nil {
		fatal(err)
	}
	checker := newChecker(*lang, *model)
	for _, gap := range checker.Missing() {
		fmt.Fprintf(os.Stderr, "check: %s\n", gap)
	}
	if checker.FFprobe == "" {
		os.Exit(1)
	}
	entries, err := os.ReadDir(folder)
	if err != nil {
		fatal(err)
	}
	verdicts := map[string]fetch.Verdict{}
	counts := map[string]int{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		verdict := checker.Check(bookFiles(filepath.Join(folder, entry.Name())))
		verdicts[entry.Name()] = verdict
		counts[verdict.Status]++
		fmt.Fprintf(os.Stderr, "%s\n        %s\n", entry.Name(), verdict.Summary())
	}
	fmt.Fprintf(os.Stderr, "\n%d books: %d ok, %d flagged, %d rejected, %d unchecked\n", len(verdicts),
		counts[fetch.VerdictOK], counts[fetch.VerdictFlagged], counts[fetch.VerdictRejected], counts[fetch.VerdictUnchecked])
	if *printJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		_ = enc.Encode(verdicts)
	}
	if counts[fetch.VerdictRejected] > 0 {
		os.Exit(1)
	}
}

// bookFiles lists the files of one book folder, hidden ones left out.
func bookFiles(folder string) []string {
	var paths []string
	_ = filepath.WalkDir(folder, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && !strings.HasPrefix(d.Name(), ".") {
			paths = append(paths, path)
		}
		return nil
	})
	return paths
}

// cmdTidy puts right, in books that are already in a folder, the two things
// a download would have had put right on arrival: archives are unpacked, and
// a book held in several formats is cut down to the best one.
func cmdTidy(args []string, cfg config) {
	flags := flag.NewFlagSet("tidy", flag.ExitOnError)
	dryRun := flags.Bool("dry-run", false, "only say what would be done")
	only := flags.String("book", "", "only tidy books whose folder name contains this")
	unpack := flags.Bool("unpack", true, "unpack zip, rar and 7z archives, then delete them")
	oneOnly := flags.Bool("keep-one-format", true, "keep the best format of a book held in several, and move the rest aside")
	flags.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s tidy [options] [FOLDER]\n\nTidies every book in FOLDER (each subfolder is one book): archives are unpacked\nand deleted, and when a book is there in several formats the best is kept and\nthe others are moved to FOLDER/.%s/duplicates. Only archives are ever deleted.\n\noptions:\n", fetch.AppName, fetch.AppName)
		flags.PrintDefaults()
	}
	positional := parseArgs(flags, args)
	if len(positional) > 1 {
		flags.Usage()
		os.Exit(2)
	}
	folder, err := destination(positional, 0, cfg.To)
	if err != nil {
		fatal(err)
	}
	unpacker := fetch.NewUnpacker()
	checker := fetch.NewChecker("", "")
	if *unpack {
		for _, gap := range unpacker.Missing() {
			fmt.Fprintf(os.Stderr, "tidy: %s\n", gap)
		}
	}
	if *oneOnly && checker.FFprobe == "" {
		fmt.Fprintf(os.Stderr, "tidy: ffprobe is not installed (part of ffmpeg), so formats cannot be compared\n")
		*oneOnly = false
	}
	entries, err := os.ReadDir(folder)
	if err != nil {
		fatal(err)
	}
	would := ""
	if *dryRun {
		would = "would be "
	}
	books, unpacked, moved, failed := 0, 0, 0, 0
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") ||
			!strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(*only)) {
			continue
		}
		books++
		paths := bookFiles(filepath.Join(folder, entry.Name()))
		var lines []string
		if *unpack && *dryRun {
			for _, path := range paths {
				if fetch.IsArchive(path) {
					unpacked++
					lines = append(lines, "would be unpacked: "+filepath.Base(path))
				}
			}
		} else if *unpack {
			files, done, notes := unpacker.Unpack(paths)
			paths = files
			unpacked += len(done)
			for _, note := range notes {
				if strings.HasPrefix(note, "could not") {
					failed++
				}
				lines = append(lines, note)
			}
		}
		if *oneOnly {
			if duplicates, note := checker.DuplicateFormats(paths); len(duplicates) > 0 {
				lines = append(lines, note)
				if !*dryRun {
					if _, err := fetch.SetAside(folder, fetch.DuplicatesDir(folder), duplicates); err != nil {
						failed++
						lines = append(lines, "could not move the duplicates aside: "+err.Error())
						duplicates = nil
					}
				}
				moved += len(duplicates)
				if len(duplicates) > 0 {
					lines = append(lines, fmt.Sprintf("%d duplicate files %smoved to %s", len(duplicates), would, fetch.DuplicatesDir(folder)))
				}
			}
		}
		if len(lines) > 0 {
			fmt.Fprintf(os.Stderr, "%s\n        %s\n", entry.Name(), strings.Join(lines, "\n        "))
		}
	}
	fmt.Fprintf(os.Stderr, "\n%d books: %d archive(s) %sunpacked, %d duplicate file(s) %smoved aside\n", books, unpacked, would, moved, would)
	if failed > 0 {
		os.Exit(1)
	}
}
