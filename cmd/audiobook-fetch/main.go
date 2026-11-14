// Command audiobook-fetch turns a list of audiobooks into files in a folder.
//
//	audiobook-fetch books.txt /path/to/folder     find each book, then download it
//	audiobook-fetch find books.txt                only find the links
//	audiobook-fetch download magnets.txt folder   only download links you already have
//
// The first form is the other two run back to back. Every form can be run
// again safely: work already done is skipped.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	fetch "github.com/broots144/audiobook-fetch"

	// Sources and downloaders register themselves when imported. To add
	// one, write its package and import it here.
	_ "github.com/broots144/audiobook-fetch/downloaders/torbox"
	_ "github.com/broots144/audiobook-fetch/sources/audiobookbay"
	_ "github.com/broots144/audiobook-fetch/sources/knaben"
	_ "github.com/broots144/audiobook-fetch/sources/piratebay"
)

const (
	defaultDownloader = "torbox"
)

// defaultSources are searched in this order: the audiobook site first, the
// general trackers only for what it could not match confidently.
var defaultSources = []string{"audiobookbay", "knaben"}

func main() {
	args := os.Args[1:]
	command := "get"
	if len(args) > 0 {
		switch args[0] {
		case "get", "find", "download", "check":
			command, args = args[0], args[1:]
		case "help", "-h", "-help", "--help":
			usage(os.Stdout)
			return
		}
	}
	cfg := loadConfig()
	switch command {
	case "find":
		cmdFind(args, cfg)
	case "download":
		cmdDownload(args, cfg)
	case "check":
		cmdCheck(args, cfg)
	default:
		cmdGet(args, cfg)
	}
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `%[1]s: a list of audiobooks in, files in a folder out.

  %[1]s LIST [FOLDER]            find each book and download it
  %[1]s find LIST                only find the links (writes a report)
  %[1]s download LINKS [FOLDER]  only download magnet links you already have
  %[1]s check [FOLDER]           check the audio of books already downloaded

LIST is a text file with one book per line, for example:

  Title | Author
  Title by Author
  Title - Author
  magnet:?xt=urn:btih:...

CSV or TSV with a header row, and JSON, work too. Use - to read from stdin.
FOLDER may be left out once "to" is set in %[2]s.

Sources:     %[3]s
Downloaders: %[4]s

Run a command with -h for its options. Everything can be run again safely:
books already found and files already downloaded are skipped.

Only use this for material you have the legal right to download.
`, fetch.AppName, filepath.Join(configDir(), "config.json"),
		strings.Join(fetch.SourceNames(), ", "), strings.Join(fetch.DownloaderNames(), ", "))
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", fetch.AppName, err)
	os.Exit(1)
}

// config holds defaults that would otherwise be typed on every run.
type config struct {
	To          string   `json:"to"`           // default download folder
	RemoveAfter bool     `json:"remove_after"` // default for -remove-after
	SearchProxy string   `json:"search_proxy"` // default for -search-proxy
	Sources     []string `json:"sources"`      // default for -source, in the order to try them
	Downloader  string   `json:"downloader"`   // default for -downloader
	// Check is the default for -check; left out, downloads are checked.
	Check        *bool  `json:"check"`
	WhisperModel string `json:"whisper_model"` // default for -whisper-model
}

func configDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "." + fetch.AppName
	}
	return filepath.Join(home, ".config", fetch.AppName)
}

func loadConfig() config {
	cfg := config{Sources: defaultSources, Downloader: defaultDownloader}
	path := filepath.Join(configDir(), "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		fatal(fmt.Errorf("%s is not valid JSON: %w", path, err))
	}
	if len(cfg.Sources) == 0 {
		cfg.Sources = defaultSources
	}
	if cfg.Downloader == "" {
		cfg.Downloader = defaultDownloader
	}
	return cfg
}

// parseArgs parses flags wherever they appear, so "LIST FOLDER -quiet" works
// as well as "-quiet LIST FOLDER", and returns what is left.
func parseArgs(flags *flag.FlagSet, args []string) []string {
	var positional []string
	for {
		_ = flags.Parse(args)
		args = flags.Args()
		if len(args) == 0 {
			return positional
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

func readInput(path string) (string, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	return string(data), err
}

// destination picks the download folder: the one named on the command line,
// else the -to flag, else the configured default.
func destination(positional []string, at int, flagValue string) (string, error) {
	if len(positional) > at {
		return positional[at], nil
	}
	if flagValue != "" {
		return flagValue, nil
	}
	return "", fmt.Errorf("no download folder given: add it after the list, or set \"to\" in %s",
		filepath.Join(configDir(), "config.json"))
}

// readKey finds a downloader's API key: the environment first (for example
// TORBOX_API_KEY), then its key file.
func readKey(downloader, keyFile string) (string, error) {
	variable := strings.ToUpper(downloader) + "_API_KEY"
	if key := strings.TrimSpace(os.Getenv(variable)); key != "" {
		return key, nil
	}
	if keyFile == "" {
		keyFile = filepath.Join(configDir(), downloader+".key")
	}
	if info, err := os.Stat(keyFile); err == nil && runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(os.Stderr, "warning: %s can be read by other users of this machine; run: chmod 600 %s\n", keyFile, keyFile)
	}
	data, err := os.ReadFile(keyFile)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("no API key for %s: set %s or put the key in %s", downloader, variable, keyFile)
	}
	if err != nil {
		return "", err
	}
	if key := strings.TrimSpace(string(data)); key != "" {
		return key, nil
	}
	return "", fmt.Errorf("%s is empty", keyFile)
}

// cmdGet is the whole job: find every book in the list, then download.
func cmdGet(args []string, cfg config) {
	var search searchFlags
	var download downloadFlags
	flags := flag.NewFlagSet(fetch.AppName, flag.ExitOnError)
	search.register(flags, cfg)
	download.register(flags, cfg)
	quiet := flags.Bool("quiet", false, "only print the summaries")
	flags.Usage = func() {
		usage(os.Stderr)
		fmt.Fprintf(os.Stderr, "\noptions:\n")
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
	download.lang = search.lang
	text, err := readInput(positional[0])
	if err != nil {
		fatal(err)
	}
	books, magnets := fetch.ParseList(text)
	if len(books)+len(magnets) == 0 {
		fatal(fmt.Errorf("nothing to do: no books or magnet links found in %s", positional[0]))
	}

	// Reports live inside the download folder, so the folder is the whole
	// record of the job and a second run picks up where the first stopped.
	reportDir := fetch.BookkeepingDir(dest)
	var results []fetch.Result
	stoppedEarly := false
	if len(books) > 0 {
		if err := search.finish(); err != nil {
			fatal(err)
		}
		results, stoppedEarly = findBooks(books, reportDir, &search, *quiet)
	}

	seen := map[string]bool{}
	var links []fetch.Magnet
	for _, m := range magnets {
		if !seen[m.Hash] {
			seen[m.Hash] = true
			links = append(links, m)
		}
	}
	for _, r := range results {
		if actionable(r, search.includeLow) && !seen[r.Candidate.InfoHash] {
			seen[r.Candidate.InfoHash] = true
			links = append(links, fetch.Magnet{Hash: r.Candidate.InfoHash, URI: r.Magnet})
		}
	}
	if len(links) == 0 {
		fmt.Fprintf(os.Stderr, "\nnothing to download. See %s for what was found.\n", filepath.Join(reportDir, "results.csv"))
		os.Exit(1)
	}

	// Download, and for every book whose listing nobody is seeding, move to
	// its next-best listing and go again until one works or none are left.
	byHash := map[string]fetch.DownloadResult{}
	searchedAgain := map[string]bool{}
	for len(links) > 0 {
		fmt.Fprintf(os.Stderr, "\ndownloading %d item(s) to %s\n", len(links), dest)
		downloads, err := downloadLinks(links, &download, *quiet)
		if err != nil {
			fatal(err)
		}
		for _, d := range downloads {
			byHash[d.Hash] = d
		}
		links = nextBestLinks(results, byHash, seen, searchedAgain, &search)
		if len(links) > 0 {
			fmt.Fprintf(os.Stderr, "\n%d book(s) had a listing with no seeds or one the check rejected; trying the next-best listing of each\n", len(links))
		}
	}
	failed := 0
	for _, d := range byHash {
		if d.Status == fetch.DownloadFailed {
			failed++
		}
	}
	if len(results) > 0 {
		if err := writeReport(reportDir, results, search.includeLow, byHash); err != nil {
			fatal(err)
		}
		attention, unseeded := 0, 0
		for _, r := range results {
			if !actionable(r, search.includeLow) {
				attention++
			} else if unusable(byHash[r.Candidate.InfoHash]) {
				unseeded++
			}
		}
		if unseeded > 0 {
			fmt.Fprintf(os.Stderr, "%d book(s) were not downloaded because no matching listing has seeds right now or passes the check; see %s\n",
				unseeded, filepath.Join(reportDir, "results.csv"))
		}
		if attention > 0 {
			fmt.Fprintf(os.Stderr, "%d book(s) were not downloaded because no confident match was found; see %s\n",
				attention, filepath.Join(reportDir, "results.csv"))
		}
	}
	if failed > 0 || stoppedEarly {
		os.Exit(1)
	}
}

// nextBestLinks moves every book whose chosen listing came back with no
// seeds on to its next-best listing, and returns the links to try now.
// A listing already tried (tried holds every hash sent so far) is passed
// over, so two books sharing a bundle do not send it twice.
//
// A book that runs out of listings is looked up once more (searchedAgain
// remembers which), on the sources its dead listing did not come from.
// Those were never asked if the first source looked certain, and a general
// tracker knows which of its listings are seeded.
func nextBestLinks(results []fetch.Result, downloads map[string]fetch.DownloadResult, tried, searchedAgain map[string]bool, s *searchFlags) []fetch.Magnet {
	var links []fetch.Magnet
	for i := range results {
		r := &results[i]
		if !actionable(*r, s.includeLow) || !unusable(downloads[r.Candidate.InfoHash]) {
			continue
		}
		why := "the first choice had no seeds"
		if downloads[r.Candidate.InfoHash].Status == fetch.DownloadRejected {
			why = "the first choice failed the check"
		}
		moved := false
		for !moved && r.NextBest(why) {
			hash := r.Candidate.InfoHash
			if !tried[hash] {
				tried[hash] = true
				links = append(links, fetch.Magnet{Hash: hash, URI: r.Magnet})
			}
			moved = !unusable(downloads[hash])
		}
		if moved || searchedAgain[r.Wanted.Raw] {
			continue
		}
		searchedAgain[r.Wanted.Raw] = true
		var others []fetch.Source
		for _, source := range s.sources {
			if source.Name() != r.Candidate.Source {
				others = append(others, source)
			}
		}
		if len(others) == 0 {
			continue
		}
		again := fetch.Find(r.Wanted, others, s.opts)
		if !actionable(again, s.includeLow) || tried[again.Candidate.InfoHash] {
			continue
		}
		again.Note = strings.TrimSuffix("found elsewhere: no usable listing on "+r.Candidate.Source+"; "+again.Note, "; ")
		*r = again
		tried[again.Candidate.InfoHash] = true
		links = append(links, fetch.Magnet{Hash: again.Candidate.InfoHash, URI: again.Magnet})
	}
	return links
}

// unusable reports that a link produced nothing to keep: nobody is seeding
// it, or what came down failed the check.
func unusable(d fetch.DownloadResult) bool {
	return d.Status == fetch.DownloadNoSeeds || d.Status == fetch.DownloadRejected
}
