package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	fetch "github.com/broots144/audiobook-fetch"
)

// downloadFlags are the options that shape the download half of the job.
type downloadFlags struct {
	opts    fetch.DownloadOptions
	name    string
	keyFile string
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
	flags.StringVar(&d.keyFile, "key-file", "", "file holding the service's API key (default: <name>.key in the config folder, or the <NAME>_API_KEY variable)")
}

// cmdDownload downloads magnet links that are already in hand.
func cmdDownload(args []string, cfg config) {
	var download downloadFlags
	flags := flag.NewFlagSet("download", flag.ExitOnError)
	download.register(flags, cfg)
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

// downloadLinks runs the download, showing progress, and prints a summary.
func downloadLinks(magnets []fetch.Magnet, d *downloadFlags, quiet bool) ([]fetch.DownloadResult, error) {
	key, err := readKey(d.name, d.keyFile)
	if err != nil {
		return nil, err
	}
	service, err := fetch.NewDownloader(d.name, key)
	if err != nil {
		return nil, err
	}
	opts := d.opts
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
	var files, fetched, removed int
	var bytes int64
	for _, r := range results {
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
