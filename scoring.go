package audiobookfetch

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Scoring: how well does a listing match the book that was asked for?
//
// Everything in this file is pure (no network). A score is 0..1. Title carries
// 70% and author 30% (title alone when no author was given), then penalties
// for wrong language / abridged / summaries / bundles. Audio quality is ranked
// separately (see Quality) and decides between listings that match equally.

const (
	titleWeight         = 0.7
	authorWeight        = 0.3
	maxFlagPenalty      = 0.35
	languagePenalty     = 0.4
	volumePenalty       = 0.15
	unknownAuthorFactor = 0.9
	partialDiscount     = 0.97 // matching only a piece of a title is worth slightly less
	headingAnywhere     = 0.8  // author found in the heading but not in the author slot
	typoMinLength       = 6
	typoMinSimilar      = 0.83
	surnameWeight       = 0.6
	givenNamesWeight    = 0.4
)

type flag struct {
	phrase string
	cost   float64
}

// Words in a listing that mean "probably not the plain unabridged book".
// Ignored when the same words are in what was asked for.
var flags = []flag{
	{"abridged", 0.25}, {"summary", 0.25}, {"study guide", 0.25},
	{"analysis", 0.25}, {"workbook", 0.25}, {"sparknotes", 0.25},
	{"cliffsnotes", 0.25}, {"excerpt", 0.25}, {"sample", 0.25},

	{"dramatization", 0.12}, {"dramatisation", 0.12}, {"dramatized", 0.12},
	{"dramatised", 0.12}, {"radio", 0.12}, {"bbc", 0.12},
	{"graphicaudio", 0.12}, {"graphic audio", 0.12}, {"collection", 0.12},
	{"box set", 0.12}, {"boxset", 0.12}, {"omnibus", 0.12},
	{"anthology", 0.12}, {"trilogy", 0.12}, {"complete series", 0.12},
}

var articles = map[string]bool{"the": true, "a": true, "an": true}

var (
	segmentSplit = regexp.MustCompile(`\s+-\s+|[:꞉;]|\s+[–—]\s+`)
	bracketed    = regexp.MustCompile(`[\(\[][^\)\]]*[\)\]]`)
	authorSplit  = regexp.MustCompile(`(?i)\s*(?:,|&|;|\band\b|\bwith\b)\s*`)
	headingSplit = regexp.MustCompile(`\s+[-–—]\s+`)
	apostrophes  = regexp.MustCompile("['’`]")
	firstNumber  = regexp.MustCompile(`\d+`)
)

// Wanted is one line of the input list.
type Wanted struct {
	Title  string `json:"title"`
	Author string `json:"author"`
	Raw    string `json:"raw"`             // the line as it was written
	Guess  string `json:"guess,omitempty"` // set when the split into title and author was inferred
}

// Candidate is one listing. Search fills the first half; the details page
// fills in the rest.
type Candidate struct {
	Source   string `json:"source"` // which Source listed it
	Heading  string `json:"heading"`
	URL      string `json:"url"`
	Language string `json:"language"`
	Format   string `json:"format"`
	Bitrate  string `json:"bitrate"`
	Size     string `json:"size"`
	Posted   string `json:"posted"`

	Authors   []string `json:"authors"`
	Narrators []string `json:"narrators"`
	Edition   string   `json:"edition"` // "Unabridged", "Abridged", "Dramatization"...
	InfoHash  string   `json:"info_hash"`
	Trackers  []string `json:"trackers"`
	// Seeders is how many peers hold the whole thing, where the source
	// says; nil where it does not. Zero means nobody can be fetched from.
	Seeders *int `json:"seeders,omitempty"`
}

// Unseeded reports that the source knows nobody is seeding the listing.
func (c *Candidate) Unseeded() bool { return c.Seeders != nil && *c.Seeders <= 0 }

// Score is the verdict on one candidate, with the parts that produced it.
type Score struct {
	Total     float64  `json:"total"`
	Title     float64  `json:"title"`
	Author    float64  `json:"author"`
	HasAuthor bool     `json:"has_author"`
	Penalty   float64  `json:"penalty"`
	Notes     []string `json:"notes"`
}

func normalize(text string) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(text) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	s := strings.ReplaceAll(b.String(), "&", " and ")
	s = apostrophes.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func tokens(text string, dropArticles bool) []string {
	var out []string
	for _, t := range strings.Fields(normalize(text)) {
		if dropArticles && articles[t] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// similarity is 1 for identical strings, 0 for nothing in common (edit distance based).
func similarity(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 && len(rb) == 0 {
		return 1
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return 1 - float64(prev[len(rb)])/float64(max(len(ra), len(rb)))
}

func isNumber(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

// sameWord tolerates a typo, but only in words long enough that it can't
// confuse two real words, and never in numbers (book 1 vs book 2).
func sameWord(a, b string) bool {
	if a == b {
		return true
	}
	if min(len([]rune(a)), len([]rune(b))) < typoMinLength || isNumber(a) || isNumber(b) {
		return false
	}
	return similarity(a, b) >= typoMinSimilar
}

func contains(pool []string, token string) bool {
	for _, p := range pool {
		if sameWord(token, p) {
			return true
		}
	}
	return false
}

func countIn(pool, items []string) int {
	n := 0
	for _, t := range items {
		if contains(pool, t) {
			n++
		}
	}
	return n
}

// textSimilarity blends word overlap (F1) with character-level similarity.
func textSimilarity(wanted, candidate string) float64 {
	w, c := tokens(wanted, true), tokens(candidate, true)
	if len(w) == 0 || len(c) == 0 {
		return 0
	}
	recall := float64(countIn(c, w)) / float64(len(w))
	precision := float64(countIn(w, c)) / float64(len(c))
	f1 := 0.0
	if recall+precision > 0 {
		f1 = 2 * recall * precision / (recall + precision)
	}
	return 0.7*f1 + 0.3*similarity(strings.Join(w, " "), strings.Join(c, " "))
}

// segments returns a title plus its pieces, e.g. for
// "Series 1 - Title: Subtitle (Unabridged)". The whole title comes first.
func segments(text string) []string {
	stripped := strings.TrimSpace(bracketed.ReplaceAllString(text, " "))
	pieces := append([]string{text, stripped}, segmentSplit.Split(stripped, -1)...)
	seen := map[string]bool{}
	var out []string
	for _, p := range pieces {
		key := normalize(p)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// MainTitle is the title without subtitle or bracketed extras.
func MainTitle(title string) string {
	stripped := strings.TrimSpace(bracketed.ReplaceAllString(title, " "))
	first := strings.TrimSpace(segmentSplit.Split(stripped, -1)[0])
	if first == "" {
		return strings.TrimSpace(title)
	}
	return first
}

// titleScore is the best match between the wanted title (with or without its
// subtitle) and the candidate title or any segment of it.
func titleScore(wantedTitle, candidateTitle string) float64 {
	type form struct {
		text   string
		factor float64
	}
	wantedForms := []form{{wantedTitle, 1}}
	if m := MainTitle(wantedTitle); normalize(m) != normalize(wantedTitle) {
		wantedForms = append(wantedForms, form{m, partialDiscount})
	}
	var candForms []form
	for i, seg := range segments(candidateTitle) {
		f := partialDiscount
		if i == 0 {
			f = 1
		}
		candForms = append(candForms, form{seg, f})
	}
	best := 0.0
	for _, w := range wantedForms {
		for _, c := range candForms {
			best = max(best, textSimilarity(w.text, c.text)*min(w.factor, c.factor))
		}
	}
	return best
}

// SplitAuthors splits "A & B", "A, B", "A and B" into names.
func SplitAuthors(author string) []string {
	var out []string
	for _, a := range authorSplit.Split(author, -1) {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// nameTokens drops initials ("J. R. R."): they vary between listings, surnames don't.
func nameTokens(name string) []string {
	var out []string
	for _, t := range tokens(name, false) {
		if len([]rune(t)) > 1 {
			out = append(out, t)
		}
	}
	return out
}

func oneAuthorScore(wantedName string, pool []string) float64 {
	name := nameTokens(wantedName)
	if len(name) == 0 || len(pool) == 0 {
		return 0
	}
	surname, given := name[len(name)-1], name[:len(name)-1]
	if len(given) == 0 {
		if contains(pool, surname) {
			return 1
		}
		return 0
	}
	score := givenNamesWeight * float64(countIn(pool, given)) / float64(len(given))
	if contains(pool, surname) {
		score += surnameWeight
	}
	return score
}

// authorScore is the best match of any wanted author against the candidate's
// author fields, falling back (at a discount) to the name appearing anywhere
// in the heading.
func authorScore(wantedAuthor string, authorTexts []string, heading string) float64 {
	names := SplitAuthors(wantedAuthor)
	var pool []string
	for _, text := range authorTexts {
		pool = append(pool, tokens(text, false)...)
	}
	anywhere := tokens(heading, false)
	best := 0.0
	for _, n := range names {
		best = max(best, oneAuthorScore(n, pool), headingAnywhere*oneAuthorScore(n, anywhere))
	}
	return best
}

// SplitHeading splits the conventional "Title - Author" heading.
func SplitHeading(heading string) (title, author string) {
	parts := headingSplit.Split(strings.TrimSpace(heading), -1)
	if len(parts) < 2 {
		return strings.TrimSpace(heading), ""
	}
	return strings.TrimSpace(strings.Join(parts[:len(parts)-1], " - ")), strings.TrimSpace(parts[len(parts)-1])
}

func hasPhrase(haystack, phrase string) bool {
	return strings.Contains(" "+haystack+" ", " "+phrase+" ")
}

func flagPenalty(w Wanted, c *Candidate) (float64, []string) {
	wantedText := normalize(w.Title + " " + w.Author)
	candText := normalize(c.Heading + " " + c.Edition)
	penalty := 0.0
	var hit []string
	for _, f := range flags {
		if hasPhrase(candText, f.phrase) && !hasPhrase(wantedText, f.phrase) {
			penalty += f.cost
			hit = append(hit, f.phrase)
		}
	}
	return min(penalty, maxFlagPenalty), hit
}

// otherVolume spots a volume number (2..30) in the candidate's title that
// wasn't asked for: "Mistborn 2" when "Mistborn" was wanted is a sequel, and
// "Books 1-3" is a bundle. Volume 1 is left alone: it is usually the book.
func otherVolume(w Wanted, candidateTitle string) string {
	wanted := tokens(w.Title, false)
	for _, t := range tokens(candidateTitle, false) {
		if n, err := strconv.Atoi(t); err == nil && n >= 2 && n <= 30 && !contains(wanted, t) {
			return t
		}
	}
	return ""
}

// Quality ranks how good the audio is, so that listings which match equally
// well can be told apart. It is bitrate weighted by how efficient the format
// is: a 64 kbps M4B sounds better than a 64 kbps MP3. An unstated bitrate
// ("Variable", "Mixed") is assumed to be modest, so a known good one wins.
func Quality(c *Candidate) float64 {
	format := strings.ToLower(c.Format)
	if strings.Contains(format, "flac") {
		return 1000
	}
	kbps := 48.0
	if n, err := strconv.Atoi(firstNumber.FindString(c.Bitrate)); err == nil && n > 0 {
		kbps = float64(min(n, 512))
	}
	for _, efficient := range []string{"m4b", "m4a", "aac", "opus", "ogg"} {
		if strings.Contains(format, efficient) {
			return kbps * 1.5
		}
	}
	return kbps
}

func round4(x float64) float64 { return math.Round(x*1e4) / 1e4 }

// ScoreCandidate rates one candidate against what was asked for. language is
// the wanted language ("english"); empty means any.
func ScoreCandidate(w Wanted, c *Candidate, language string) Score {
	candTitle, candAuthor := SplitHeading(c.Heading)

	// Also try the whole heading, in case its " - " was part of the title.
	t := max(titleScore(w.Title, candTitle), titleScore(w.Title, c.Heading)*partialDiscount)

	s := Score{Title: round4(t), Notes: []string{}}
	base := t
	if strings.TrimSpace(w.Author) != "" {
		a := authorScore(w.Author, append([]string{candAuthor}, c.Authors...), c.Heading)
		if a == 0 && candAuthor == "" && len(c.Authors) == 0 {
			// The listing names no author anywhere. That is not evidence of
			// the wrong author, so judge it on title, a little cautiously.
			base = t * unknownAuthorFactor
			s.Notes = append(s.Notes, "no author listed")
		} else {
			s.Author, s.HasAuthor = round4(a), true
			base = titleWeight*t + authorWeight*a
		}
	}

	penalty, hit := flagPenalty(w, c)
	if len(hit) > 0 {
		s.Notes = append(s.Notes, "flags: "+strings.Join(hit, ", "))
	}
	if v := otherVolume(w, candTitle); v != "" {
		penalty += volumePenalty
		s.Notes = append(s.Notes, "volume "+v)
	}
	if language != "" && c.Language != "" && normalize(c.Language) != normalize(language) {
		penalty += languagePenalty
		s.Notes = append(s.Notes, "language: "+c.Language)
	}
	s.Penalty = round4(penalty)
	s.Total = round4(max(0, min(1, base-penalty)))
	return s
}
