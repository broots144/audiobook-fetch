package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	fetch "github.com/broots144/audiobook-fetch"
)

// cmdDoctor looks over the setup and says, line by line, what is in place
// and what is not, with the fix for each gap. It reports whether everything
// the tool cannot work without is there; the optional tools never fail it.
func cmdDoctor(args []string, cfg config, out io.Writer) (ready bool) {
	flags := flag.NewFlagSet("doctor", flag.ExitOnError)
	offline := flags.Bool("offline", false, "do not contact the downloader to try the key")
	name := flags.String("downloader", cfg.Downloader, "service to check the key of")
	keyFile := flags.String("key-file", "", "file holding the service's API key")
	flags.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s doctor [options]\n\nChecks the setup: config, download folder, the downloader's key, and the\noptional programs for checking audio and unpacking archives.\n\noptions:\n", fetch.AppName)
		flags.PrintDefaults()
	}
	_ = flags.Parse(args)

	ready = true
	line := func(mark, what, detail string) { fmt.Fprintf(out, "  %-4s %-18s %s\n", mark, what, detail) }
	ok := func(what, detail string) { line("ok", what, detail) }
	optional := func(what, detail string) { line("--", what, detail) }
	missing := func(what, detail string) { line("MISS", what, detail); ready = false }

	fmt.Fprintf(out, "%s %s\n\nNeeded:\n", fetch.AppName, versionString())

	// The downloader and its key.
	key, err := readKey(*name, *keyFile)
	service, serviceErr := fetch.Downloader(nil), error(nil)
	if err == nil {
		service, serviceErr = fetch.NewDownloader(*name, key)
	}
	// Books from the Internet Archive are fetched from it directly, with no
	// account, so a missing key only rules out the other sources.
	keyless := "public-domain books from the Internet Archive still download without one"
	switch {
	case err != nil:
		missing("downloader key", err.Error()+" ("+keyless+")")
	case serviceErr != nil:
		missing("downloader", serviceErr.Error())
	case *offline:
		ok("downloader key", "found for "+service.Name()+" (not tried: -offline)")
	default:
		if items, err := service.List(); err != nil {
			missing("downloader key", service.Name()+" did not accept it: "+err.Error())
		} else {
			ok("downloader key", fmt.Sprintf("%s accepts it (%d items in the account)", service.Name(), len(items)))
		}
	}

	// Sources.
	var unknown []string
	for _, source := range cfg.Sources {
		if _, err := fetch.NewSource(source, fetch.SourceConfig{}); err != nil {
			unknown = append(unknown, source)
		}
	}
	if len(unknown) > 0 {
		missing("sources", fmt.Sprintf("config names %s; available: %s", strings.Join(unknown, ", "), strings.Join(fetch.SourceNames(), ", ")))
	} else {
		ok("sources", strings.Join(cfg.Sources, ", "))
	}

	fmt.Fprintf(out, "\nOptional:\n")
	configFile := filepath.Join(configDir(), "config.json")
	if _, err := os.Stat(configFile); err == nil {
		ok("config file", configFile)
	} else {
		optional("config file", "none at "+configFile+" (defaults are used)")
	}
	switch info, err := os.Stat(cfg.To); {
	case cfg.To == "":
		optional("download folder", `not set: name it on the command line, or set "to" in the config file`)
	case err != nil:
		optional("download folder", cfg.To+" cannot be reached right now: "+err.Error())
	case !info.IsDir():
		optional("download folder", cfg.To+" is not a folder")
	default:
		ok("download folder", cfg.To)
	}

	// The audio check.
	checker := fetch.NewChecker("", defaultModel(cfg))
	tool := func(what, path, without string) {
		if path != "" {
			ok(what, path)
		} else {
			optional(what, "not found: "+without)
		}
	}
	tool("ffprobe", checker.FFprobe, "downloads are not checked at all (install ffmpeg)")
	tool("ffmpeg", checker.FFmpeg, "the spoken language is not checked (install ffmpeg)")
	tool("whisper-cli", checker.Whisper, "the spoken language is not checked (install whisper-cpp)")
	tool("whisper model", checker.Model, "the spoken language is not checked (put a ggml-*.bin in "+filepath.Join(configDir(), "models")+")")

	// Archives.
	unpacker := fetch.NewUnpacker()
	var unpackers []string
	for _, path := range []string{unpacker.SevenZip, unpacker.Unar, unpacker.BSDTar} {
		if path != "" {
			unpackers = append(unpackers, path)
		}
	}
	if len(unpackers) > 0 {
		ok("rar and 7z", strings.Join(unpackers, ", "))
	} else {
		optional("rar and 7z", "not found: such archives are left packed (install 7zz, 7z, unar or bsdtar); zip needs nothing")
	}

	if ready {
		fmt.Fprintf(out, "\nReady. Lines marked -- are optional extras that are not set up.\n")
	} else {
		fmt.Fprintf(out, "\nNot ready: fix the lines marked MISS.\n")
	}
	return ready
}
