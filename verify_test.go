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
}

func (f *fakeTools) run(name string, args ...string) ([]byte, []byte, error) {
	last := args[len(args)-1]
	switch name {
	case "ffprobe":
		if f.broken[filepath.Base(last)] {
			return nil, nil, errors.New("invalid data")
		}
		out := fmt.Sprintf(`{"streams":[{"codec_name":"mp3","codec_type":"audio"},{"codec_name":"png","codec_type":"video"}],
			"format":{"duration":"3600.0","bit_rate":"%d","tags":{"Title":%q}}}`, f.kbps*1000, f.tags[filepath.Base(last)])
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
