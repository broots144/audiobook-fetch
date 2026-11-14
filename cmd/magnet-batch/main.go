// Command magnet-batch reads a list of "Title | Author" lines and writes the
// magnet link of the best match for each.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	mb "magnetbatch"
)

// giveUpAfter is how many books in a row may fail before the run stops.
const giveUpAfter = 3

func main() {
	opts := mb.DefaultOptions()
	client := mb.NewClient()

	outDir := flag.String("out", "magnet-results", "directory for magnets.txt, results.csv and results.json")
	includeLow := flag.Bool("include-low", false, "also put low-confidence picks in magnets.txt")
	resume := flag.Bool("resume", false, "reuse matches already in the output directory and only look up the rest")
	printJSON := flag.Bool("json", false, "print the full results as JSON on stdout")
	quiet := flag.Bool("quiet", false, "no per-book progress lines")
	lang := flag.String("lang", opts.Language, `wanted language, or "any"`)
	flag.Float64Var(&opts.MinScore, "min-score", opts.MinScore, "score (0-1) a pick needs to count as a match")
	flag.IntVar(&opts.Top, "top", opts.Top, "how many front-runners to inspect in detail before choosing")
	flag.IntVar(&opts.Pages, "pages", opts.Pages, "result pages to read per search")
	flag.DurationVar(&client.Delay, "delay", client.Delay, "pause between requests")
	flag.StringVar(&client.BaseURL, "base-url", client.BaseURL, "site to search (or set MAGNET_BATCH_BASE_URL)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: magnet-batch [flags] books.txt   (use - to read the list from stdin)\n\n"+
			"Each line of the list is: Title | Author   (author optional, # for comments)\n\nflags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	if strings.EqualFold(*lang, "any") {
		*lang = ""
	}
	opts.Language = *lang
	client.BaseURL = strings.TrimRight(client.BaseURL, "/")

	list, err := readList(flag.Arg(0))
	if err != nil {
		fatal(err)
	}
	if len(list) == 0 {
		fatal(fmt.Errorf("no books found in %s", flag.Arg(0)))
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fatal(err)
	}

	previous := map[string]mb.Result{}
	if *resume {
		previous = loadMatches(filepath.Join(*outDir, "results.json"))
	}

	start := time.Now()
	results := make([]mb.Result, 0, len(list))
	failures, stoppedAt := 0, -1
	for i, w := range list {
		r, reused := previous[w.Raw]
		if !reused {
			r = mb.FindMagnet(w, client, opts)
		}
		if r.Status == mb.StatusError {
			failures++
		} else {
			failures = 0
		}
		if failures >= giveUpAfter {
			// The site is down or has stopped answering us. Don't grind
			// through the rest of the list at minutes per book.
			stoppedAt = i - failures + 1
			results = results[:len(results)-(failures-1)]
			_ = writeOutputs(*outDir, results, *includeLow)
			break
		}
		results = append(results, r)
		if !*quiet {
			progress(i+1, len(list), r, reused)
		}
		// Rewritten after every book so an interrupted run loses nothing.
		if err := writeOutputs(*outDir, results, *includeLow); err != nil {
			fatal(err)
		}
	}

	summary(results, *outDir, *includeLow, client.Requests, time.Since(start))
	if stoppedAt >= 0 {
		fmt.Fprintf(os.Stderr, "\nstopped early: the site failed %d books in a row, starting at entry %d of %d (%q).\n"+
			"Wait a while, then run again with -resume to pick up where this left off.\n",
			giveUpAfter, stoppedAt+1, len(list), list[stoppedAt].Raw)
		os.Exit(1)
	}
	if *printJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		_ = enc.Encode(results)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "magnet-batch:", err)
	os.Exit(1)
}

func readList(path string) ([]mb.Wanted, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	return mb.ParseList(string(data)), nil
}

func loadMatches(path string) map[string]mb.Result {
	out := map[string]mb.Result{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var saved []mb.Result
	if json.Unmarshal(data, &saved) != nil {
		return out
	}
	for _, r := range saved {
		if r.Status == mb.StatusMatched {
			out[r.Wanted.Raw] = r
		}
	}
	return out
}

func inMagnetFile(r mb.Result, includeLow bool) bool {
	return r.Magnet != "" && (r.Status == mb.StatusMatched || (includeLow && r.Status == mb.StatusLowConfidence))
}

func writeOutputs(dir string, results []mb.Result, includeLow bool) error {
	// Two books can resolve to the same listing (a bundle, say); list it once.
	var magnets strings.Builder
	listed := map[string]bool{}
	for _, r := range results {
		if inMagnetFile(r, includeLow) && !listed[r.Candidate.InfoHash] {
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
	_ = w.Write([]string{"input", "status", "score", "matched", "authors", "narrators", "edition",
		"language", "format", "bitrate", "size", "runners_up", "note", "url", "magnet"})
	for _, r := range results {
		c := r.Candidate
		if c == nil {
			c = &mb.Candidate{}
		}
		var runners []string
		for _, ru := range r.RunnersUp {
			runners = append(runners, fmt.Sprintf("%.2f %s", ru.Score, ru.Heading))
		}
		_ = w.Write([]string{r.Wanted.Raw, r.Status, fmt.Sprintf("%.2f", r.Score), c.Heading,
			strings.Join(c.Authors, ", "), strings.Join(c.Narrators, ", "), c.Edition,
			c.Language, c.Format, c.Bitrate, c.Size, strings.Join(runners, " / "), r.Note, c.URL, r.Magnet})
	}
	w.Flush()
	return w.Error()
}

var marks = map[string]string{
	mb.StatusMatched:       "✓",
	mb.StatusLowConfidence: "?",
	mb.StatusNotFound:      "✗",
	mb.StatusError:         "!",
}

func progress(n, total int, r mb.Result, reused bool) {
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
		line += "  (from previous run)"
	}
	fmt.Fprintln(os.Stderr, line)
}

func summary(results []mb.Result, dir string, includeLow bool, requests int, took time.Duration) {
	counts := map[string]int{}
	written := 0
	for _, r := range results {
		counts[r.Status]++
		if inMagnetFile(r, includeLow) {
			written++
		}
	}
	fmt.Fprintf(os.Stderr, "\n%d books: %d matched, %d low confidence, %d not found, %d errors  (%d requests, %s)\n",
		len(results), counts[mb.StatusMatched], counts[mb.StatusLowConfidence],
		counts[mb.StatusNotFound], counts[mb.StatusError], requests, took.Round(time.Second))
	fmt.Fprintf(os.Stderr, "%d magnet links → %s\n", written, filepath.Join(dir, "magnets.txt"))
	fmt.Fprintf(os.Stderr, "full report     → %s\n", filepath.Join(dir, "results.csv"))
	if counts[mb.StatusLowConfidence] > 0 && !includeLow {
		fmt.Fprintln(os.Stderr, "low-confidence picks are in the report but not in magnets.txt (-include-low adds them)")
	}
}
