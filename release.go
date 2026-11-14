package audiobookfetch

import (
	"regexp"
	"strings"
)

// Release names: general trackers have no fields for author, edition or
// audio format. Everything is packed into one name, such as
// "Author - Title (Unabridged) [MP3 64kbps]". ParseRelease unpacks it.

var (
	releaseBitrate = regexp.MustCompile(`(?i)\b(\d{2,3})\s*k(?:bps|b/s|bit/s|b)?\b`)
	releaseFormat  = regexp.MustCompile(`(?i)\b(m4b|m4a|mp3|flac|aac|opus|ogg)\b`)
	releaseNoise   = regexp.MustCompile(`(?i)\b(audio\s?books?|unabridged|mp3|m4b|m4a|flac|aac|opus|ogg|\d{2,3}\s*k(?:bps|b/s|bit/s|b)?|vbr|cbr|retail|repack)\b`)
	emptyBrackets  = regexp.MustCompile(`[\(\[\{][\s,;:._/+&-]*[\)\]\}]`)
	looseDashes    = regexp.MustCompile(`(\s+[-–—])+\s*$|^\s*([-–—]\s+)+`)
	manySpaces     = regexp.MustCompile(`\s{2,}`)
)

// Words in a release name that give its language away. General trackers
// have no language field, so this is all there is to go on. A language's
// English name only counts as a tag, in brackets, because book titles are
// full of such words ("The French Lieutenant's Woman"); its name for itself,
// or its word for "audiobook", counts anywhere.
var releaseLanguages = []struct{ language, tag, anywhere string }{
	{"German", `german`, `deutsch|h[oö]rbuch|h[oö]rspiel`},
	{"French", `french`, `fran[cç]ais|livre audio`},
	{"Spanish", `spanish`, `espa[nñ]ol|castellano|audiolibro`},
	{"Italian", `italian`, `italiano`},
	{"Dutch", `dutch`, `nederlands|luisterboek`},
	{"Finnish", `finnish`, `suomi|[aä][aä]nikirja`},
	{"Swedish", `swedish`, `svenska|ljudbok`},
	{"Norwegian", `norwegian`, `norsk|lydbok`},
	{"Danish", `danish`, `dansk|lydbog`},
	{"Polish", `polish`, `polski`},
	{"Russian", `russian`, `аудиокнига`},
	{"Portuguese", `portuguese`, `portugu[eê]s|audiolivro`},
	{"English", `english`, `\bENG\b`},
}

var releaseLanguage = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(releaseLanguages))
	for i, l := range releaseLanguages {
		out[i] = regexp.MustCompile(`(?i)[\(\[\{][^\)\]\}]*\b(` + l.tag + `)\b[^\(\[\{]*[\)\]\}]|(^|[^\pL])(` + l.anywhere + `)([^\pL]|$)`)
	}
	return out
}()

// ParseRelease fills a candidate from a tracker release name: the heading
// with the technical clutter removed, plus whatever the name gives away
// about language, edition, format and bitrate. Words that count against a listing
// (abridged, dramatized...) are left in the heading for the scorer to see.
func ParseRelease(name string, c *Candidate) {
	text := strings.TrimSpace(name)
	if !strings.Contains(text, " ") { // Scene.Style.Names or under_scores
		text = strings.NewReplacer(".", " ", "_", " ").Replace(text)
	}
	if m := releaseFormat.FindStringSubmatch(text); m != nil {
		c.Format = strings.ToUpper(m[1])
	}
	if m := releaseBitrate.FindStringSubmatch(text); m != nil {
		c.Bitrate = m[1] + " Kbps"
	}
	for i, pattern := range releaseLanguage {
		if pattern.MatchString(text) {
			c.Language = releaseLanguages[i].language
			break
		}
	}
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "unabridged"):
		c.Edition = "Unabridged"
	case strings.Contains(lower, "abridged"):
		c.Edition = "Abridged"
	}
	text = releaseNoise.ReplaceAllString(text, " ")
	for emptyBrackets.MatchString(text) {
		text = emptyBrackets.ReplaceAllString(text, " ")
	}
	text = manySpaces.ReplaceAllString(text, " ")
	text = looseDashes.ReplaceAllString(text, "")
	if text = strings.TrimSpace(text); text == "" {
		text = strings.TrimSpace(name)
	}
	c.Heading = text
}

// HumanSize renders a byte count the way listings usually show it.
func HumanSize(n int64) string {
	if n <= 0 {
		return ""
	}
	return humanBytes(n)
}
