package audiobookfetch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTools stands in for ffprobe, ffmpeg and whisper-cli.
type fakeTools struct {
	broken   map[string]bool   // files ffprobe cannot read
	tags     map[string]string // file -> title tag
	kbps     int
	heard    []string // language detections, in the order they are asked for
	opening  string   // what the transcribed opening contains
	listened int
	// Per file extension, for books that come in several formats.
	seconds map[string]float64
	kbpsFor map[string]int
}

func (f *fakeTools) run(name string, args ...string) ([]byte, []byte, error) {
	last := args[len(args)-1]
	switch name {
	case "ffprobe":
		if f.broken[filepath.Base(last)] {
			return nil, nil, errors.New("invalid data")
		}
		ext := strings.ToLower(filepath.Ext(last))
		codec, seconds, kbps := "mp3", 3600.0, f.kbps
		if ext == ".m4b" || ext == ".m4a" {
			codec = "aac"
		}
		if s, ok := f.seconds[ext]; ok {
			seconds = s
		}
		if k, ok := f.kbpsFor[ext]; ok {
			kbps = k
		}
		out := fmt.Sprintf(`{"streams":[{"codec_name":%q,"codec_type":"audio"},{"codec_name":"png","codec_type":"video"}],
			"format":{"duration":"%.1f","bit_rate":"%d","tags":{"Title":%q}}}`, codec, seconds, kbps*1000, f.tags[filepath.Base(last)])
		return []byte(out), nil, nil
	case "ffmpeg":
		return nil, nil, nil
	case "whisper-cli":
		if args[len(args)-1] == "-dl" {
			heard := f.heard[f.listened%len(f.heard)]
			f.listened++
			return nil, []byte("whisper_full_with_state: auto-detected language: " + heard + "\n"), nil
		}
		return []byte(f.opening), nil, nil
	}
	return nil, nil, errors.New("unexpected tool " + name)
}

func checkerWith(tools *fakeTools) *Checker {
	return &Checker{Language: "english", FFprobe: "ffprobe", FFmpeg: "ffmpeg", Whisper: "whisper-cli", Model: "model.bin",
		Parallel: 2, run: tools.run}
}

func bookFiles(t *testing.T, names ...string) []string {
	dir := t.TempDir()
	var paths []string
	for _, name := range names {
		path := filepath.Join(dir, "Some Book", name)
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	return paths
}

func TestCheckPassesAPlainEnglishBook(t *testing.T) {
	tools := &fakeTools{kbps: 64, heard: []string{"en (p = 0.99)"}, opening: "Chapter one. It was a dark night."}
	v := checkerWith(tools).Check(bookFiles(t, "01.mp3", "02.mp3", "cover.jpg"))
	if v.Status != VerdictOK || v.AudioFiles != 2 || v.Hours != 2 || v.Kbps != 64 || v.Codec != "mp3" || v.Language != "en" {
		t.Errorf("verdict: %+v", v)
	}
	if !strings.Contains(v.Summary(), "2 audio files, 2.0 h, 64 kbps mp3") {
		t.Errorf("summary: %s", v.Summary())
	}
}

func TestCheckRejectsWhatIsNotAnAudiobookInTheWantedLanguage(t *testing.T) {
	// Another language, heard in most samples.
	tools := &fakeTools{kbps: 64, heard: []string{"de (p = 0.97)", "de (p = 0.91)", "en (p = 0.60)"}}
	v := checkerWith(tools).Check(bookFiles(t, "01.mp3"))
	if v.Status != VerdictRejected || !strings.Contains(v.Reasons[0], "spoken language is de, not en (2 of 3 samples)") {
		t.Errorf("another language: %+v", v)
	}

	// One odd sample (music, a quoted phrase) is not enough to throw a book out.
	tools = &fakeTools{kbps: 64, heard: []string{"fr (p = 0.80)", "en (p = 0.20)", "en (p = 0.30)"}}
	v = checkerWith(tools).Check(bookFiles(t, "01.mp3"))
	if v.Status != VerdictFlagged || !strings.Contains(v.Flags[0], "may be fr") {
		t.Errorf("one unsure sample: %+v", v)
	}

	// Nothing to play.
	v = checkerWith(&fakeTools{}).Check(bookFiles(t, "readme.txt", "setup.exe"))
	if v.Status != VerdictRejected || !strings.Contains(v.Reasons[0], "no audio files") {
		t.Errorf("no audio: %+v", v)
	}
	tools = &fakeTools{broken: map[string]bool{"01.mp3": true}}
	v = checkerWith(tools).Check(bookFiles(t, "01.mp3"))
	if v.Status != VerdictRejected || !strings.Contains(v.Reasons[0], "can be played") {
		t.Errorf("unplayable: %+v", v)
	}
}

func TestCheckFlagsEditionsThatAreNotThePlainReading(t *testing.T) {
	tools := &fakeTools{kbps: 24, heard: []string{"en (p = 0.99)"},
		opening: "This is an abridged recording. A full cast dramatization of the unabridged novel.",
		tags:    map[string]string{"01.mp3": "Some Book (BBC Radio Drama)"},
		broken:  map[string]bool{"03.mp3": true}}
	v := checkerWith(tools).Check(bookFiles(t, "01.mp3", "02 unabridged.mp3", "03.mp3"))
	if v.Status != VerdictFlagged {
		t.Fatalf("verdict: %+v", v)
	}
	flags := strings.Join(v.Flags, " | ")
	for _, want := range []string{"1 of 3 audio files cannot be played", "low bitrate (24 kbps)", `a file's tags says "radio drama"`,
		`the opening says "abridged"`, `the opening says "full cast"`, `the opening says "dramatization"`} {
		if !strings.Contains(flags, want) {
			t.Errorf("missing flag %q in: %s", want, flags)
		}
	}
	if strings.Contains(flags, "unabridged") || strings.Contains(v.Summary(), "recording") {
		t.Errorf("only the telling words may be kept, never the transcript: %s", v.Summary())
	}
}

func TestCheckSaysWhatItCouldNotDo(t *testing.T) {
	tools := &fakeTools{kbps: 64}
	c := checkerWith(tools)
	c.Whisper, c.Model = "", ""
	v := c.Check(bookFiles(t, "01.mp3"))
	if v.Status != VerdictOK || len(v.Skipped) != 1 || !strings.Contains(v.Skipped[0], "whisper-cli is not installed") || v.Language != "" {
		t.Errorf("without whisper: %+v", v)
	}
	c.FFprobe = ""
	if v = c.Check(bookFiles(t, "01.mp3")); v.Status != VerdictUnchecked || !strings.Contains(v.Skipped[0], "ffprobe is not installed") {
		t.Errorf("without ffprobe: %+v", v)
	}
}

func TestCheckKeepsOneFormatOfABookHeldInSeveral(t *testing.T) {
	names := func(dir, ext string, n int) []string {
		var out []string
		for i := 1; i <= n; i++ {
			out = append(out, fmt.Sprintf("%s/%02d%s", dir, i, ext))
		}
		return out
	}
	// The whole book three times: one M4B, ten MP3s, ten M4As. The M4B is
	// the worst-sounding by far; the other two sound alike, and the M4As
	// are smaller.
	tools := &fakeTools{heard: []string{"en (p = 0.99)"},
		seconds: map[string]float64{".m4b": 36000, ".mp3": 3600, ".m4a": 3605},
		kbpsFor: map[string]int{".m4b": 17, ".mp3": 47, ".m4a": 32}}
	files := bookFiles(t, append(append([]string{"book.m4b", "cover.jpg"}, names("MP3", ".mp3", 10)...), names("MP3", ".m4a", 10)...)...)
	v := checkerWith(tools).Check(files)
	if v.Status != VerdictOK || v.AudioFiles != 10 || v.Hours != 10 || v.Kbps != 32 || v.Codec != "aac" || len(v.Duplicates) != 11 {
		t.Fatalf("verdict: %+v", v)
	}
	want := "the same book in 3 formats: keeping m4a (10 files, 32 kbps); duplicates: mp3 (10 files, 47 kbps), m4b (1 file, 17 kbps)"
	if len(v.Notes) != 1 || v.Notes[0] != want || !strings.Contains(v.Summary(), want) {
		t.Errorf("note: %q", v.Notes)
	}
	for _, path := range v.Duplicates {
		if ext := filepath.Ext(path); ext != ".mp3" && ext != ".m4b" {
			t.Errorf("%s is not a duplicate", path)
		}
	}
	if duplicates, note := checkerWith(tools).DuplicateFormats(files); len(duplicates) != 11 || note != want {
		t.Errorf("DuplicateFormats: %d files, %q", len(duplicates), note)
	}

	// When the formats sound as good as each other, the single M4B wins.
	tools.kbpsFor = map[string]int{".m4b": 64, ".mp3": 96, ".m4a": 64}
	v = checkerWith(tools).Check(files)
	if v.AudioFiles != 1 || len(v.Duplicates) != 20 || !strings.Contains(v.Notes[0], "keeping m4b (1 file, 64 kbps)") {
		t.Errorf("equal quality: %+v", v)
	}
}

func TestCheckLeavesAloneWhatIsNotARepeat(t *testing.T) {
	for name, c := range map[string]struct {
		files   []string
		seconds map[string]float64
	}{
		"one format in two folders":      {[]string{"CD1/01.mp3", "CD1/02.mp3", "CD2/01.mp3", "CD2/02.mp3"}, nil},
		"a book and a shorter extra":     {[]string{"book.m4b", "interview.mp3"}, map[string]float64{".m4b": 36000, ".mp3": 1800}},
		"two halves in different format": {[]string{"part1.mp3", "part2.m4a"}, map[string]float64{".mp3": 18000, ".m4a": 16000}},
		"an incomplete second copy":      {[]string{"book.m4b", "01.mp3", "02.mp3"}, map[string]float64{".m4b": 36000, ".mp3": 17000}},
		"two short clips":                {[]string{"a.mp3", "a.m4a"}, map[string]float64{".mp3": 300, ".m4a": 300}},
	} {
		tools := &fakeTools{kbps: 64, heard: []string{"en (p = 0.99)"}, seconds: c.seconds}
		v := checkerWith(tools).Check(bookFiles(t, c.files...))
		if len(v.Duplicates) != 0 || len(v.Notes) != 0 || v.AudioFiles != len(c.files) {
			t.Errorf("%s: %+v", name, v)
		}
	}
}

func TestCheckDoesNotRejectABookStillInsideAnArchive(t *testing.T) {
	v := checkerWith(&fakeTools{}).Check(bookFiles(t, "book.rar", "readme.txt"))
	if v.Status != VerdictUnchecked || len(v.Skipped) != 1 || !strings.Contains(v.Skipped[0], "inside 1 archive(s)") {
		t.Errorf("verdict: %+v", v)
	}
}

func TestCheckTreatsASecondEncodingOfTheSameFormatAsARepeat(t *testing.T) {
	// As the Internet Archive ships a book: every chapter as the original
	// MP3, again at 64 kbps, and again as Ogg.
	tools := &fakeTools{kbps: 128, heard: []string{"en (p = 0.99)"}, kbpsFor: map[string]int{".ogg": 70}}
	v := checkerWith(tools).Check(bookFiles(t, "ch_01.mp3", "ch_02.mp3", "ch_01_64kb.mp3", "ch_02_64kb.mp3", "ch_01.ogg", "ch_02.ogg", "cover.png"))
	if v.AudioFiles != 2 || len(v.Duplicates) != 4 || !strings.Contains(v.Notes[0], "the same book in 3 formats: keeping mp3 (2 files, 128 kbps)") {
		t.Fatalf("verdict: %+v", v)
	}
	for _, path := range v.Duplicates {
		if name := filepath.Base(path); name == "ch_01.mp3" || name == "ch_02.mp3" {
			t.Errorf("%s is the copy to keep", name)
		}
	}
}
