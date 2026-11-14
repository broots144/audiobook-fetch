package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	fetch "github.com/broots144/audiobook-fetch"
)

// giveUpAfter is how many books in a row may fail before a search run stops.
const giveUpAfter = 3

// searchFlags are the options that shape the search half of the job.
type searchFlags struct {
	opts       fetch.Options
	sources    []fetch.Source
	names      string
	lang       string
	proxy      string
	delay      time.Duration
	includeLow bool
	fresh      bool
}

func (s *searchFlags) register(flags *flag.FlagSet, cfg config) {
	s.opts = fetch.DefaultOptions()
	flags.StringVar(&s.names, "source", strings.Join(cfg.Sources, ","),
		"where to search, comma-separated in the order to try ("+strings.Join(fetch.SourceNames(), ", ")+")")
	flags.StringVar(&s.lang, "lang", s.opts.Language, `wanted language, or "any"`)
	flags.Float64Var(&s.opts.MinScore, "min-score", s.opts.MinScore, "score (0-1) a pick needs to count as a match")
	flags.BoolVar(&s.includeLow, "include-low", false, "also accept low-confidence picks")
	flags.BoolVar(&s.fresh, "fresh", false, "look every book up again instead of reusing earlier matches")
	flags.IntVar(&s.opts.Top, "top", s.opts.Top, "front-runners to inspect in detail before choosing")
	flags.IntVar(&s.opts.Pages, "pages", s.opts.Pages, "result pages to read per search")
	flags.DurationVar(&s.delay, "delay", 3*time.Second, "pause between requests to a source")
	flags.StringVar(&s.proxy, "search-proxy", cfg.SearchProxy, "send searches through this proxy, e.g. socks5://127.0.0.1:1080")
}

// finish turns the parsed flags into ready-to-use sources.
func (s *searchFlags) finish() error {
	s.opts.Language = s.lang
	if strings.EqualFold(s.lang, "any") {
		s.opts.Language = ""
	}
	for _, name := range strings.Split(s.names, ",") {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		source, err := fetch.NewSource(name, fetch.SourceConfig{Delay: s.delay, Proxy: s.proxy})
		if err != nil {
			return err
		}
		s.sources = append(s.sources, source)
	}
	if len(s.sources) == 0 {
		return fmt.Errorf("no source to search: name one with -source (%s)", strings.Join(fetch.SourceNames(), ", "))
	}
	return nil
}

// cmdFind looks every book up and writes the report, without downloading.
func cmdFind(args []string, cfg config) {
	var search searchFlags
	flags := flag.NewFlagSet("find", flag.ExitOnError)
	search.register(flags, cfg)
	outDir := flags.String("out", "results", "folder for magnets.txt, results.csv and results.json")
	printJSON := flags.Bool("json", false, "print the full results as JSON on stdout")
	quiet := flags.Bool("quiet", false, "only print the summary")
	flags.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s find [options] LIST\n\nFinds the best link for each book in LIST and writes a report.\n\noptions:\n", fetch.AppName)
		flags.PrintDefaults()
	}
	positional := parseArgs(flags, args)
	if len(positional) != 1 {
		flags.Usage()
		os.Exit(2)
	}
	if err := search.finish(); err != nil {
		fatal(err)
	}
	text, err := readInput(positional[0])
	if err != nil {
		fatal(err)
	}
	books, _ := fetch.ParseList(text)
	if len(books) == 0 {
		fatal(fmt.Errorf("no books found in %s", positional[0]))
	}
	results, stoppedEarly := findBooks(books, *outDir, &search, *quiet)
	if *printJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		_ = enc.Encode(results)
	}
	if stoppedEarly {
		os.Exit(1)
	}
}

// findBooks looks up every book, writing the report to dir as it goes so an
// interrupted run loses nothing. Matches from an earlier run are reused.
// stoppedEarly reports that the sources stopped answering.
func findBooks(books []fetch.Wanted, dir string, s *searchFlags, quiet bool) (results []fetch.Result, stoppedEarly bool) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fatal(err)
	}
	previous := map[string]fetch.Result{}
	if !s.fresh {
		previous = loadMatches(filepath.Join(dir, "results.json"))
	}

	start := time.Now()
	failures := 0
	for i, w := range books {
		r, reused := previous[w.Raw]
		if !reused {
			r = fetch.Find(w, s.sources, s.opts)
		}
		if r.Status == fetch.StatusError {
			failures++
		} else {
			failures = 0
		}
		if failures >= giveUpAfter {
			// The source is down or has stopped answering. Drop the failures
			// (so the next run retries them) rather than grind on at
			// minutes per book.
			results = results[:len(results)-(failures-1)]
			stoppedEarly = true
			break
		}
		results = append(results, r)
		if !quiet {
			progress(i+1, len(books), r, reused)
		}
		if err := writeReport(dir, results, s.includeLow, nil); err != nil {
			fatal(err)
		}
	}
	if err := writeReport(dir, results, s.includeLow, nil); err != nil {
		fatal(err)
	}

	counts := map[string]int{}
	for _, r := range results {
		counts[r.Status]++
	}
	fmt.Fprintf(os.Stderr, "\n%d of %d books looked up: %d matched, %d low confidence, %d not found, %d errors  (%s)\n",
		len(results), len(books), counts[fetch.StatusMatched], counts[fetch.StatusLowConfidence],
		counts[fetch.StatusNotFound], counts[fetch.StatusError], time.Since(start).Round(time.Second))
	fmt.Fprintf(os.Stderr, "report: %s\n", filepath.Join(dir, "results.csv"))
	if stoppedEarly {
		fmt.Fprintf(os.Stderr, "stopped early: searching failed for %d books in a row. Wait a while and run the same command again;\n"+
			"it will carry on from book %d. If this network is being refused, -search-proxy sends searches another way.\n",
			giveUpAfter, len(results)+1)
	}
	return results, stoppedEarly
}

// loadMatches returns the confident matches of an earlier run, by input line.
func loadMatches(path string) map[string]fetch.Result {
	out := map[string]fetch.Result{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var saved []fetch.Result
	if json.Unmarshal(data, &saved) != nil {
		return out
	}
	for _, r := range saved {
		if r.Status == fetch.StatusMatched {
			out[r.Wanted.Raw] = r
		}
	}
	return out
}

// actionable reports whether a result's link is one to act on.
func actionable(r fetch.Result, includeLow bool) bool {
	return r.Magnet != "" && (r.Status == fetch.StatusMatched || (includeLow && r.Status == fetch.StatusLowConfidence))
}

// writeReport writes magnets.txt, results.json and results.csv. downloads,
// when given, adds what became of each link.
func writeReport(dir string, results []fetch.Result, includeLow bool, downloads map[string]fetch.DownloadResult) error {
	// Two books can resolve to the same listing (a bundle, say); list it once.
	var magnets strings.Builder
	listed := map[string]bool{}
	for _, r := range results {
		if actionable(r, includeLow) && !listed[r.Candidate.InfoHash] {
			listed[r.Candidate.InfoHash] = true
			magnets.WriteString(r.Magnet + "\n")
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "magnets.txt"), []byte(magnets.String()), 0o644); err != nil {
		return err
	}

	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "results.json"), data, 0o644); err != nil {
		return err
	}

	file, err := os.Create(filepath.Join(dir, "results.csv"))
	if err != nil {
		return err
	}
	defer file.Close()
	w := csv.NewWriter(file)
	_ = w.Write([]string{"input", "status", "download", "score", "matched", "source", "authors", "narrators",
		"edition", "language", "format", "bitrate", "size", "runners_up", "note", "url", "magnet"})
	for _, r := range results {
		c := r.Candidate
		if c == nil {
			c = &fetch.Candidate{}
		}
		var runners []string
		for _, ru := range r.RunnersUp {
			runners = append(runners, fmt.Sprintf("%.2f %s", ru.Score, ru.Heading))
		}
		download := ""
		if d, ok := downloads[c.InfoHash]; ok && actionable(r, includeLow) {
			download = d.Status
		}
		_ = w.Write(plainCells([]string{r.Wanted.Raw, r.Status, download, fmt.Sprintf("%.2f", r.Score), c.Heading, c.Source,
			strings.Join(c.Authors, ", "), strings.Join(c.Narrators, ", "), c.Edition,
			c.Language, c.Format, c.Bitrate, c.Size, strings.Join(runners, " / "), r.Note, c.URL, r.Magnet}))
	}
	w.Flush()
	return w.Error()
}

// plainCells keeps a spreadsheet from running a cell as a formula. Headings
// come from web pages, and one starting with = + - or @ would be executed
// when the report is opened; a leading apostrophe makes it plain text.
func plainCells(cells []string) []string {
	for i, cell := range cells {
		if cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
			cells[i] = "'" + cell
		}
	}
	return cells
}

var marks = map[string]string{
	fetch.StatusMatched:       "✓",
	fetch.StatusLowConfidence: "?",
	fetch.StatusNotFound:      "✗",
	fetch.StatusError:         "!",
}

func progress(n, total int, r fetch.Result, reused bool) {
	line := fmt.Sprintf("[%d/%d] %s %.2f  %s", n, total, marks[r.Status], r.Score, r.Wanted.Raw)
	switch {
	case r.Magnet != "":
		var extras []string
		for _, e := range []string{r.Candidate.Edition, r.Candidate.Format, r.Candidate.Size} {
			if e != "" {
				extras = append(extras, e)
			}
		}
		line += "\n        → " + r.Candidate.Heading
		if len(extras) > 0 {
			line += "  (" + strings.Join(extras, ", ") + ")"
		}
		if r.Note != "" {
			line += "  [" + r.Note + "]"
		}
	case r.Note != "":
		line += "\n        " + r.Note
	}
	if reused {
		line += "  (from an earlier run)"
	}
	fmt.Fprintln(os.Stderr, line)
}
