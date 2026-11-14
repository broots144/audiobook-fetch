package audiobookfetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Checking: downloaded files -> is this really the audiobook that was asked
// for? A listing's description is only a claim. Once the files are here the
// audio itself can be asked: does it play, how good is it, what language is
// spoken, and does it announce itself as abridged or dramatized.
//
// The work is done by two outside programs, both optional: ffprobe (part of
// ffmpeg) reads the files, and whisper-cli (whisper.cpp) listens to short
// samples. Whatever is missing is skipped and said so, never guessed.

const (
	VerdictOK        = "ok"
	VerdictFlagged   = "flagged"   // kept, but something deserves a look
	VerdictRejected  = "rejected"  // not what was asked for
	VerdictUnchecked = "unchecked" // nothing could be checked

	sampleLength    = 30 * time.Second  // what language detection listens to
	introLength     = 120 * time.Second // where an edition announces itself
	languageSure    = 0.5               // below this a detection does not count
	lowBitrateKbps  = 32
	toolTimeout     = 3 * time.Minute
	defaultProbeFan = 4
)

// Verdict is what checking one downloaded item found.
type Verdict struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons,omitempty"` // why it was rejected
	Flags   []string `json:"flags,omitempty"`   // what deserves a look
	Skipped []string `json:"skipped,omitempty"` // checks that could not run

	AudioFiles int     `json:"audio_files"`
	Unplayable int     `json:"unplayable,omitempty"`
	Hours      float64 `json:"hours"`
	Kbps       int     `json:"kbps"`
	Codec      string  `json:"codec,omitempty"`
	Language   string  `json:"language,omitempty"` // as heard, e.g. "en"
}

// Summary is the verdict on one line.
func (v Verdict) Summary() string {
	parts := []string{v.Status}
	if v.AudioFiles > 0 {
		parts = append(parts, fmt.Sprintf("%d audio files, %.1f h, %d kbps %s", v.AudioFiles, v.Hours, v.Kbps, v.Codec))
	}
	if v.Language != "" {
		parts = append(parts, "spoken language "+v.Language)
	}
	for _, list := range [][]string{v.Reasons, v.Flags} {
		if len(list) > 0 {
			parts = append(parts, strings.Join(list, "; "))
		}
	}
	if len(v.Skipped) > 0 {
		parts = append(parts, "not checked: "+strings.Join(v.Skipped, "; "))
	}
	return strings.Join(parts, " | ")
}

// Checker checks downloaded items. The zero value checks nothing; use
// NewChecker, which finds the tools.
type Checker struct {
	Language string // wanted language ("english"); "" accepts any
	FFprobe  string // path to ffprobe; "" if not installed
	FFmpeg   string // path to ffmpeg, needed to cut samples
	Whisper  string // path to whisper-cli
	Model    string // path to a whisper.cpp model file
	Parallel int    // files probed at once

	run func(name string, args ...string) (stdout, stderr []byte, err error)
}

// NewChecker returns a Checker using whichever tools are on the PATH. model
// is the whisper.cpp model file; "" or a missing file turns listening off.
func NewChecker(language, model string) *Checker {
	find := func(name string) string {
		path, _ := exec.LookPath(name)
		return path
	}
	c := &Checker{Language: language, FFprobe: find("ffprobe"), FFmpeg: find("ffmpeg"), Whisper: find("whisper-cli"),
		Parallel: defaultProbeFan}
	if info, err := os.Stat(model); err == nil && !info.IsDir() {
		c.Model = model
	}
	return c
}

// Missing says, in plain words, which checks cannot run on this machine.
func (c *Checker) Missing() []string {
	var out []string
	if c.FFprobe == "" {
		out = append(out, "ffprobe is not installed (part of ffmpeg), so nothing can be checked")
		return out
	}
	switch {
	case c.FFmpeg == "":
		out = append(out, "spoken language and edition: ffmpeg is not installed")
	case c.Whisper == "":
		out = append(out, "spoken language and edition: whisper-cli is not installed (whisper.cpp)")
	case c.Model == "":
		out = append(out, "spoken language and edition: no whisper.cpp model file")
	}
	return out
}

func (c *Checker) canListen() bool {
	return c.FFmpeg != "" && c.Whisper != "" && c.Model != ""
}

func (c *Checker) exec(name string, args ...string) ([]byte, []byte, error) {
	if c.run != nil {
		return c.run(name, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

var audioExtensions = map[string]bool{".mp3": true, ".m4b": true, ".m4a": true, ".flac": true, ".ogg": true,
	".opus": true, ".aac": true, ".wav": true, ".wma": true, ".mp4": true, ".mka": true}

// Words by which a recording says it is not the plain, full reading. \b
// keeps "unabridged" from matching "abridged".
var editionWords = regexp.MustCompile(`(?i)\b(abridged|abridgement|dramati[sz]ation|dramati[sz]ed|full[- ]cast|graphic\s?audio|radio (?:drama|play|adaptation))\b`)

// whisper.cpp names languages by code.
var languageCodes = map[string]string{
	"english": "en", "german": "de", "french": "fr", "spanish": "es", "italian": "it", "dutch": "nl",
	"finnish": "fi", "swedish": "sv", "norwegian": "no", "danish": "da", "polish": "pl", "russian": "ru",
	"portuguese": "pt", "japanese": "ja", "chinese": "zh", "korean": "ko", "turkish": "tr", "greek": "el",
	"czech": "cs", "hungarian": "hu", "arabic": "ar", "hebrew": "he", "hindi": "hi",
}

// probed is what ffprobe says about one file.
type probed struct {
	path     string
	seconds  float64
	bitsPerS float64
	codec    string
	tags     string // title, album and comment, for the edition words
}

// Check examines the files of one downloaded item.
func (c *Checker) Check(paths []string) Verdict {
	var v Verdict
	var audio []string
	for _, path := range paths {
		if audioExtensions[strings.ToLower(filepath.Ext(path))] {
			audio = append(audio, path)
		}
	}
	sort.Strings(audio)
	v.AudioFiles = len(audio)
	if len(audio) == 0 {
		v.Status = VerdictRejected
		v.Reasons = []string{fmt.Sprintf("no audio files among the %d downloaded", len(paths))}
		return v
	}
	if c.FFprobe == "" {
		v.Status, v.Skipped = VerdictUnchecked, c.Missing()
		return v
	}

	files := c.probeAll(audio)
	var playable []probed
	var seconds, bits float64
	codecs := map[string]float64{}
	for _, f := range files {
		if f.seconds <= 0 {
			v.Unplayable++
			continue
		}
		playable = append(playable, f)
		seconds += f.seconds
		bits += f.bitsPerS * f.seconds
		codecs[f.codec] += f.seconds
	}
	if len(playable) == 0 {
		v.Status = VerdictRejected
		v.Reasons = []string{fmt.Sprintf("none of the %d audio files can be played", len(audio))}
		return v
	}
	v.Hours = float64(int(seconds/360+0.5)) / 10
	v.Kbps = int(bits/seconds/1000 + 0.5)
	for codec, share := range codecs {
		if share > codecs[v.Codec] || v.Codec == "" {
			v.Codec = codec
		}
	}
	if v.Unplayable > 0 {
		v.Flags = append(v.Flags, fmt.Sprintf("%d of %d audio files cannot be played", v.Unplayable, len(audio)))
	}
	if v.Kbps > 0 && v.Kbps < lowBitrateKbps {
		v.Flags = append(v.Flags, fmt.Sprintf("low bitrate (%d kbps)", v.Kbps))
	}

	// What the files call themselves.
	said := map[string]bool{}
	note := func(where, text string) {
		for _, word := range editionWords.FindAllString(text, -1) {
			word = strings.ToLower(word)
			if !said[word] {
				said[word] = true
				v.Flags = append(v.Flags, fmt.Sprintf("%s says %q", where, word))
			}
		}
	}
	for _, f := range playable {
		note("a file name", strings.NewReplacer("_", " ", ".", " ").Replace(filepath.Base(filepath.Dir(f.path))+" / "+filepath.Base(f.path)))
		note("a file's tags", f.tags)
	}

	// What the audio itself says.
	if !c.canListen() {
		v.Skipped = c.Missing()
	} else {
		c.listen(playable, seconds, &v, note)
	}

	switch {
	case len(v.Reasons) > 0:
		v.Status = VerdictRejected
	case len(v.Flags) > 0:
		v.Status = VerdictFlagged
	default:
		v.Status = VerdictOK
	}
	return v
}

func (c *Checker) probeAll(audio []string) []probed {
	out := make([]probed, len(audio))
	slots := make(chan struct{}, max(1, c.Parallel))
	var wg sync.WaitGroup
	for i, path := range audio {
		wg.Add(1)
		slots <- struct{}{}
		go func(i int, path string) {
			defer wg.Done()
			defer func() { <-slots }()
			out[i] = c.probe(path)
		}(i, path)
	}
	wg.Wait()
	return out
}

// probe asks ffprobe about one file. A file it cannot read, or with no
// audio in it, comes back with zero seconds.
func (c *Checker) probe(path string) probed {
	f := probed{path: path}
	// A downloaded file is untrusted, and some formats (playlists) can name
	// other things to open. Only local files are ever read.
	stdout, _, err := c.exec(c.FFprobe, "-v", "error", "-protocol_whitelist", "file", "-show_entries",
		"format=duration,bit_rate:format_tags=title,album,comment:stream=codec_name,codec_type", "-of", "json", path)
	if err != nil {
		return f
	}
	var data struct {
		Format struct {
			Duration string            `json:"duration"`
			BitRate  string            `json:"bit_rate"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
		Streams []struct {
			Name string `json:"codec_name"`
			Type string `json:"codec_type"`
		} `json:"streams"`
	}
	if json.Unmarshal(stdout, &data) != nil {
		return f
	}
	for _, stream := range data.Streams {
		if stream.Type == "audio" {
			f.codec = stream.Name
			break
		}
	}
	if f.codec == "" {
		return f
	}
	f.seconds, _ = strconv.ParseFloat(data.Format.Duration, 64)
	f.bitsPerS, _ = strconv.ParseFloat(data.Format.BitRate, 64)
	for key, value := range data.Format.Tags {
		switch strings.ToLower(key) {
		case "title", "album", "comment":
			f.tags += value + " / "
		}
	}
	return f
}

// listen samples the audio: three half-minutes spread through the book for
// the spoken language, and the opening for how the edition announces itself.
func (c *Checker) listen(files []probed, total float64, v *Verdict, note func(where, text string)) {
	tmp, err := os.MkdirTemp("", AppName+"-check-")
	if err != nil {
		v.Skipped = append(v.Skipped, "spoken language and edition: "+err.Error())
		return
	}
	defer os.RemoveAll(tmp)
	wav := filepath.Join(tmp, "sample.wav")

	// One sample can land on music or silence, so three are taken and the
	// book is only rejected when most of them agree it is another language.
	votes := map[string]int{}
	heard := 0
	for _, at := range []float64{0.2, 0.5, 0.8} {
		file, offset := locate(files, total*at)
		if c.cut(file.path, offset, sampleLength, wav) != nil {
			continue
		}
		_, stderr, _ := c.exec(c.Whisper, "-m", c.Model, "-f", wav, "-dl")
		if m := detectedLanguage.FindSubmatch(stderr); m != nil {
			if sure, _ := strconv.ParseFloat(string(m[2]), 64); sure >= languageSure {
				votes[string(m[1])]++
				heard++
			}
		}
	}
	for code, n := range votes {
		if n > votes[v.Language] || v.Language == "" {
			v.Language = code
		}
	}
	wanted := languageCodes[strings.ToLower(strings.TrimSpace(c.Language))]
	switch {
	case heard == 0:
		v.Skipped = append(v.Skipped, "spoken language: no clear speech in the samples")
	case c.Language == "":
	case wanted == "":
		v.Skipped = append(v.Skipped, "spoken language: do not know how "+c.Language+" sounds")
	case v.Language != wanted && votes[v.Language] >= 2:
		v.Reasons = append(v.Reasons, fmt.Sprintf("spoken language is %s, not %s (%d of %d samples)",
			v.Language, wanted, votes[v.Language], heard))
		return
	case v.Language != wanted:
		v.Flags = append(v.Flags, fmt.Sprintf("spoken language may be %s, not %s (%d of %d samples)",
			v.Language, wanted, votes[v.Language], heard))
	}

	// The opening is transcribed only to look for those few words; the
	// transcript itself is thrown away.
	code := v.Language
	if code == "" {
		code = "en"
	}
	if c.cut(files[0].path, 0, introLength, wav) == nil {
		if stdout, _, err := c.exec(c.Whisper, "-m", c.Model, "-f", wav, "-l", code, "-nt", "-np"); err == nil {
			note("the opening", string(stdout))
		}
	}
}

var detectedLanguage = regexp.MustCompile(`auto-detected language:\s*([a-z]{2,3})\s*\(p\s*=\s*([0-9.]+)\)`)

// locate finds the file playing at a point in the whole book, and where in
// that file, leaving room for a sample before the file ends.
func locate(files []probed, at float64) (probed, float64) {
	for _, f := range files {
		if at < f.seconds {
			return f, max(0, min(at, f.seconds-sampleLength.Seconds()))
		}
		at -= f.seconds
	}
	last := files[len(files)-1]
	return last, max(0, last.seconds-sampleLength.Seconds())
}

// cut writes length of audio from path, starting at offset seconds, as the
// 16 kHz mono WAV whisper.cpp wants.
func (c *Checker) cut(path string, offset float64, length time.Duration, wav string) error {
	_, _, err := c.exec(c.FFmpeg, "-v", "error", "-y", "-protocol_whitelist", "file", "-ss", strconv.FormatFloat(offset, 'f', 1, 64),
		"-t", strconv.Itoa(int(length.Seconds())), "-i", path, "-vn", "-ac", "1", "-ar", "16000", "-f", "wav", wav)
	return err
}
