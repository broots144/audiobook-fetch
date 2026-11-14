package magnetbatch

import (
	"errors"
	"strings"
	"testing"
)

func total(title, author, heading string) float64 {
	return ScoreCandidate(Wanted{Title: title, Author: author}, &Candidate{Heading: heading, Language: "English"}, "english").Total
}

func TestScoreAcceptsRealMatches(t *testing.T) {
	cases := []struct{ title, author, heading string }{
		{"Dune", "Frank Herbert", "Dune - Frank Herbert"},
		{"Dune", "Frank Herbert", "Dune: Book One (Unabridged) - Frank Herbert"},
		{"The Way of Kings", "Brandon Sanderson", "The Stormlight Archive 1 - The Way of Kings - Brandon Sanderson"},
		{"Sapiens: A Brief History of Humankind", "Yuval Noah Harari", "Sapiens - Yuval Noah Harari"},
		{"Les Misérables", "Victor Hugo", "Les Miserables - Victor Hugo"},
		{"The Hobbit", "J.R.R. Tolkien", "The Hobbit - J. R. R. Tolkien"},
		{"The Way of Kings", "Brandon Sandersen", "The Way of Kings - Brandon Sanderson"},
		{"Good Omens", "Neil Gaiman & Terry Pratchett", "Good Omens - Terry Pratchett, Neil Gaiman"},
		{"Dune", "", "Dune - Frank Herbert"},
	}
	for _, c := range cases {
		if got := total(c.title, c.author, c.heading); got < 0.9 {
			t.Errorf("%q | %q vs %q = %.2f, want >= 0.9", c.title, c.author, c.heading, got)
		}
	}
}

func TestScoreRejectsWrongBooks(t *testing.T) {
	minScore := DefaultOptions().MinScore
	cases := []struct{ title, author, heading string }{
		{"Dune", "Frank Herbert", "Dune Messiah - Frank Herbert"},
		{"Dune", "Frank Herbert", "Dune - Somebody Else"},
		{"Mistborn", "Brandon Sanderson", "Mistborn 2: The Well of Ascension - Brandon Sanderson"},
		{"Pride and Prejudice", "Jane Austen", "Jane Austen꞉ Eight Classic BBC Radio Dramas - Jane Austen"},
		{"Dune", "Frank Herbert", "Summary of Dune - Frank Herbert"},
		{"Emma", "Jane Austen", "Persuasion - Jane Austen"},
	}
	for _, c := range cases {
		if got := total(c.title, c.author, c.heading); got >= minScore {
			t.Errorf("%q | %q vs %q = %.2f, want < %.2f", c.title, c.author, c.heading, got, minScore)
		}
	}
}

func TestScorePrefersPlainEnglishUnabridged(t *testing.T) {
	w := Wanted{Title: "Dune", Author: "Frank Herbert"}
	plain := ScoreCandidate(w, &Candidate{Heading: "Dune - Frank Herbert", Language: "English"}, "english")

	abridged := ScoreCandidate(w, &Candidate{Heading: "Dune - Frank Herbert", Language: "English", Edition: "Abridged"}, "english")
	if abridged.Total >= plain.Total {
		t.Errorf("abridged %.2f should score below plain %.2f", abridged.Total, plain.Total)
	}
	unabridged := ScoreCandidate(w, &Candidate{Heading: "Dune - Frank Herbert", Language: "English", Edition: "Unabridged"}, "english")
	if unabridged.Total != plain.Total {
		t.Errorf("unabridged %.2f should not be penalised (plain %.2f)", unabridged.Total, plain.Total)
	}
	german := &Candidate{Heading: "Dune - Frank Herbert", Language: "German"}
	if got := ScoreCandidate(w, german, "english").Total; got >= DefaultOptions().MinScore {
		t.Errorf("wrong language scored %.2f", got)
	}
	if got := ScoreCandidate(w, german, "").Total; got != plain.Total {
		t.Errorf("language should be ignored when any is allowed, got %.2f", got)
	}
}

func TestFlagsIgnoredWhenAskedFor(t *testing.T) {
	if got := total("The Complete BBC Radio Collection", "Douglas Adams", "The Complete BBC Radio Collection - Douglas Adams"); got < 0.9 {
		t.Errorf("flag words that were asked for should not be penalised, got %.2f", got)
	}
}

func TestDetailsAuthorsRescueAHeadingWithoutAuthor(t *testing.T) {
	w := Wanted{Title: "Dune", Author: "Frank Herbert"}
	c := &Candidate{Heading: "Dune", Language: "English"}
	before := ScoreCandidate(w, c, "english").Total
	c.Authors = []string{"Frank Herbert"}
	after := ScoreCandidate(w, c, "english").Total
	if before < 0.85 || before >= 0.95 || after < 0.95 {
		t.Errorf("no author listed %.2f (want a cautious 0.85-0.95), confirmed author %.2f (want >= 0.95)", before, after)
	}
}

func TestParseList(t *testing.T) {
	got := ParseList("# my books\n\nDune | Frank Herbert\n  The Hobbit|J.R.R. Tolkien  \nNo Author Here\nTabbed\tSome One\n | orphan author\n")
	want := []Wanted{
		{"Dune", "Frank Herbert", "Dune | Frank Herbert"},
		{"The Hobbit", "J.R.R. Tolkien", "The Hobbit|J.R.R. Tolkien"},
		{"No Author Here", "", "No Author Here"},
		{"Tabbed", "Some One", "Tabbed\tSome One"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestBuildQueries(t *testing.T) {
	cases := []struct {
		w    Wanted
		want string
	}{
		{Wanted{Title: "The Hobbit", Author: "J.R.R. Tolkien"}, "Hobbit Tolkien|Hobbit"},
		{Wanted{Title: "Sapiens: A Brief History of Humankind", Author: "Yuval Noah Harari"}, "Sapiens Harari|Sapiens"},
		{Wanted{Title: "Ender's Game", Author: "Orson Scott Card"}, "Ender Game Card|Ender Game"},
		{Wanted{Title: "Ender’s Game", Author: "Orson Scott Card"}, "Ender Game Card|Ender Game"},
		{Wanted{Title: "Dune"}, "Dune"},
	}
	for _, c := range cases {
		if got := strings.Join(BuildQueries(c.w), "|"); got != c.want {
			t.Errorf("BuildQueries(%+v) = %q, want %q", c.w, got, c.want)
		}
	}
}

func TestBuildMagnet(t *testing.T) {
	got := BuildMagnet("abc123", "A Title & More", []string{"udp://t.example:80/announce"})
	want := "magnet:?xt=urn:btih:abc123&dn=A%20Title%20%26%20More&tr=udp%3A%2F%2Ft.example%3A80%2Fannounce"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if !strings.Contains(BuildMagnet("abc123", "x", nil), "&tr=") {
		t.Error("expected fallback trackers when none are given")
	}
}

const searchHTML = `<div id="content">
<div class="post"><div class="postTitle"><h2><a href="/abss/first-book/" rel="bookmark">First Book - Ann Author</a></h2></div>
<div class="postInfo">Category: Adults&nbsp; Classic&nbsp; <br />Language: English<span>Keywords: One&nbsp Two&nbsp </span><br /></div>
<div class="postContent"><p>Shared by:<a href="/member">someone</a></p>
<p>Posted: 16 Dec 2025<br />Format: <span>M4B</span> / Bitrate: <span>64 Kbps</span><br />File Size: <span>419.23</span> MBs</p></div></div>
<div class="post"><div class="postTitle"><h2><a href="https://other.example/abss/second/">Second Book - B. Writer</a></h2></div>
<div class="postInfo">Category: Kids <br />Language: German<br /></div>
<div class="postContent"><p>Posted: 1 Jan 2024<br />Format: <span>MP3</span> / Bitrate: <span>Mixed</span><br />File Size: <span>1.67</span> GBs</p></div></div>
<div class="post"><div class="postTitle"><h2>no link here</h2></div></div>
</div>`

func TestParseSearch(t *testing.T) {
	got, err := ParseSearch(searchHTML, "https://site.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2", len(got))
	}
	first := Candidate{Heading: "First Book - Ann Author", URL: "https://site.example/abss/first-book/",
		Language: "English", Format: "M4B", Bitrate: "64 Kbps", Size: "419.23 MBs", Posted: "16 Dec 2025"}
	if g := *got[0]; g.Heading != first.Heading || g.URL != first.URL || g.Language != first.Language ||
		g.Format != first.Format || g.Bitrate != first.Bitrate || g.Size != first.Size || g.Posted != first.Posted {
		t.Errorf("first = %+v\nwant   %+v", g, first)
	}
	if g := got[1]; g.URL != "https://other.example/abss/second/" || g.Language != "German" || g.Bitrate != "Mixed" || g.Size != "1.67 GBs" {
		t.Errorf("second = %+v", *g)
	}
}

const detailsHTML = `<div class="post"><div class="postTitle"><h1 itemprop="name">First Book: Full Heading - Ann Author</h1></div>
<div class="desc"><p>Written by <a href="/?s=ann"><span class="author">Ann Author</span></a> <br />
Read by <a href="/?s=r1"><span class="narrator">Reader One</span></a>, <a href="/?s=r2"><span class="narrator">Reader Two</span></a><br />
Format: <span class="format">M4B</span> <br /><span class="is_abridged">Unabridged</span></p></div>
<table>
<tr><td>Tracker:</td><td>udp://one.example:1337/announce</td></tr>
<tr><td>Tracker:</td><td>http://two.example:80/announce</td></tr>
<tr><td>Tracker:</td><td>udp://one.example:1337/announce</td></tr>
<tr><td colspan='2'>This is a Multifile Torrent</td></tr>
<tr><td>Info Hash:</td><td>6D9A901038D432F3EC1DF3E7906CEDEECCEFD5E8</td></tr>
</table>
<ul><li><span class="commentAuthor">not an author</span></li></ul></div>`

func TestParseDetails(t *testing.T) {
	c := &Candidate{Heading: "First Book - Ann Author"}
	if err := ParseDetails(detailsHTML, c); err != nil {
		t.Fatal(err)
	}
	if c.Heading != "First Book: Full Heading - Ann Author" {
		t.Errorf("heading = %q", c.Heading)
	}
	if strings.Join(c.Authors, "|") != "Ann Author" || strings.Join(c.Narrators, "|") != "Reader One|Reader Two" {
		t.Errorf("authors = %v, narrators = %v", c.Authors, c.Narrators)
	}
	if c.Edition != "Unabridged" {
		t.Errorf("edition = %q", c.Edition)
	}
	if c.InfoHash != "6d9a901038d432f3ec1df3e7906cedeeccefd5e8" {
		t.Errorf("hash = %q", c.InfoHash)
	}
	if strings.Join(c.Trackers, "|") != "udp://one.example:1337/announce|http://two.example:80/announce" {
		t.Errorf("trackers = %v", c.Trackers)
	}
}

// fakeSource answers searches from a fixed table and applies canned details.
type fakeSource struct {
	listings map[string][]Candidate // query -> listings
	details  map[string]Candidate   // url -> fields the details page adds
	searches []string
	fetched  []string
	failWith error
}

func (f *fakeSource) Search(query string, page int) ([]*Candidate, error) {
	f.searches = append(f.searches, query)
	if f.failWith != nil {
		return nil, f.failWith
	}
	if page > 1 {
		return nil, nil
	}
	var out []*Candidate
	for _, c := range f.listings[query] {
		c := c
		out = append(out, &c)
	}
	return out, nil
}

func (f *fakeSource) Details(c *Candidate) error {
	f.fetched = append(f.fetched, c.URL)
	d := f.details[c.URL]
	c.Authors, c.Edition, c.InfoHash, c.Trackers = d.Authors, d.Edition, d.InfoHash, d.Trackers
	return nil
}

func TestFindMagnetPicksUnabridgedAfterCheckingDetails(t *testing.T) {
	src := &fakeSource{
		listings: map[string][]Candidate{"Dune Herbert": {
			{Heading: "Dune - Frank Herbert", URL: "u/abridged", Language: "English"},
			{Heading: "Dune - Frank Herbert", URL: "u/full", Language: "English"},
			{Heading: "Dune Messiah - Frank Herbert", URL: "u/sequel", Language: "English"},
			{Heading: "Dune - Frank Herbert", URL: "u/german", Language: "German"},
		}},
		details: map[string]Candidate{
			"u/abridged": {Authors: []string{"Frank Herbert"}, Edition: "Abridged", InfoHash: "aaa"},
			"u/full":     {Authors: []string{"Frank Herbert"}, Edition: "Unabridged", InfoHash: "bbb", Trackers: []string{"udp://t.example:1/announce"}},
		},
	}
	r := FindMagnet(Wanted{Title: "Dune", Author: "Frank Herbert"}, src, DefaultOptions())

	if r.Status != StatusMatched || r.Candidate.URL != "u/full" {
		t.Fatalf("status %s, picked %+v", r.Status, r.Candidate)
	}
	if !strings.HasPrefix(r.Magnet, "magnet:?xt=urn:btih:bbb&dn=Dune%20-%20Frank%20Herbert&tr=udp%3A%2F%2Ft.example") {
		t.Errorf("magnet = %s", r.Magnet)
	}
	if strings.Join(src.fetched, ",") != "u/abridged,u/full" {
		t.Errorf("details fetched for %v, want only the two close front-runners", src.fetched)
	}
	if len(src.searches) != 1 {
		t.Errorf("searches = %v, want to stop after the first good one", src.searches)
	}
	if len(r.RunnersUp) == 0 || r.RunnersUp[0].Heading != "Dune - Frank Herbert" {
		t.Errorf("runners up = %+v", r.RunnersUp)
	}
}

func TestFindMagnetFallsBackToTitleOnlySearch(t *testing.T) {
	src := &fakeSource{
		listings: map[string][]Candidate{"Dune": {{Heading: "Dune", URL: "u/1", Language: "English"}}},
		details:  map[string]Candidate{"u/1": {Authors: []string{"Frank Herbert"}, InfoHash: "ccc"}},
	}
	r := FindMagnet(Wanted{Title: "Dune", Author: "Frank Herbert"}, src, DefaultOptions())
	if r.Status != StatusMatched || strings.Join(src.searches, "|") != "Dune Herbert|Dune" {
		t.Errorf("status %s (%.2f), searches %v", r.Status, r.Score, src.searches)
	}
}

func TestFindMagnetOutcomesWithoutAGoodMatch(t *testing.T) {
	w := Wanted{Title: "Dune", Author: "Frank Herbert"}

	empty := FindMagnet(w, &fakeSource{}, DefaultOptions())
	if empty.Status != StatusNotFound || empty.Magnet != "" {
		t.Errorf("no results: %+v", empty)
	}

	unrelated := &fakeSource{listings: map[string][]Candidate{"Dune": {{Heading: "Gardening Basics - A. Person", URL: "u/x"}}}}
	r := FindMagnet(w, unrelated, DefaultOptions())
	if r.Status != StatusNotFound || r.Magnet != "" || len(unrelated.fetched) != 0 {
		t.Errorf("unrelated result: status %s, magnet %q, fetched %v", r.Status, r.Magnet, unrelated.fetched)
	}

	sequelOnly := &fakeSource{
		listings: map[string][]Candidate{"Dune Herbert": {{Heading: "Dune Messiah - Frank Herbert", URL: "u/s", Language: "English"}}},
		details:  map[string]Candidate{"u/s": {Authors: []string{"Frank Herbert"}, InfoHash: "ddd"}},
	}
	r = FindMagnet(w, sequelOnly, DefaultOptions())
	if r.Status != StatusLowConfidence || r.Magnet == "" {
		t.Errorf("sequel only: status %s (%.2f), magnet %q", r.Status, r.Score, r.Magnet)
	}

	noHash := &fakeSource{listings: map[string][]Candidate{"Dune Herbert": {{Heading: "Dune - Frank Herbert", URL: "u/n"}}}}
	if r = FindMagnet(w, noHash, DefaultOptions()); r.Status != StatusNotFound {
		t.Errorf("no hash: status %s", r.Status)
	}

	broken := FindMagnet(w, &fakeSource{failWith: errors.New("HTTP 503")}, DefaultOptions())
	if broken.Status != StatusError || broken.Note != "HTTP 503" {
		t.Errorf("error: %+v", broken)
	}
}

func TestFindMagnetPicksBestAudioAmongEqualMatches(t *testing.T) {
	same := "Dune - Frank Herbert"
	src := &fakeSource{
		listings: map[string][]Candidate{"Dune Herbert": {
			{Heading: same, URL: "u/mp3-64", Language: "English", Format: "MP3", Bitrate: "64 Kbps"},
			{Heading: same, URL: "u/unknown", Language: "English", Format: "MP3", Bitrate: "Variable"},
			{Heading: same, URL: "u/m4b-128", Language: "English", Format: "M4B", Bitrate: "128 Kbps"},
			{Heading: same, URL: "u/mp3-128", Language: "English", Format: "MP3", Bitrate: "128 Kbps"},
			{Heading: same, URL: "u/german-320", Language: "German", Format: "M4B", Bitrate: "320 Kbps"},
			{Heading: "Dune Messiah - Frank Herbert", URL: "u/sequel-320", Language: "English", Format: "M4B", Bitrate: "320 Kbps"},
			{Heading: same, URL: "u/abridged-256", Language: "English", Format: "M4B", Bitrate: "256 Kbps"},
		}},
		details: map[string]Candidate{
			"u/mp3-64": {InfoHash: "a"}, "u/unknown": {InfoHash: "b"}, "u/m4b-128": {InfoHash: "c"},
			"u/mp3-128": {InfoHash: "d"}, "u/abridged-256": {InfoHash: "e", Edition: "Abridged"},
		},
	}
	r := FindMagnet(Wanted{Title: "Dune", Author: "Frank Herbert"}, src, DefaultOptions())
	if r.Status != StatusMatched || r.Candidate.URL != "u/m4b-128" {
		t.Fatalf("status %s, picked %s (fetched %v)", r.Status, r.Candidate.URL, src.fetched)
	}
	for _, u := range src.fetched {
		if u == "u/german-320" || u == "u/sequel-320" {
			t.Errorf("should never have looked at %s", u)
		}
	}
}

func TestFindMagnetIsStrictAboutLanguage(t *testing.T) {
	src := &fakeSource{listings: map[string][]Candidate{"Dune Herbert": {
		{Heading: "Dune - Frank Herbert", URL: "u/de", Language: "German"},
		{Heading: "Dune - Frank Herbert", URL: "u/es", Language: "Spanish"},
	}}}
	w := Wanted{Title: "Dune", Author: "Frank Herbert"}
	r := FindMagnet(w, src, DefaultOptions())
	if r.Status != StatusNotFound || r.Magnet != "" || len(src.fetched) != 0 || !strings.Contains(r.Note, "other languages") {
		t.Errorf("english only: %+v, fetched %v", r, src.fetched)
	}

	any := DefaultOptions()
	any.Language = ""
	src.details = map[string]Candidate{"u/de": {InfoHash: "a"}, "u/es": {InfoHash: "b"}}
	if r = FindMagnet(w, src, any); r.Status != StatusMatched {
		t.Errorf("any language: status %s", r.Status)
	}
}

func TestQuality(t *testing.T) {
	order := []Candidate{
		{Format: "FLAC"},
		{Format: "M4B", Bitrate: "128 Kbps"},
		{Format: "MP3", Bitrate: "128 Kbps"},
		{Format: "M4B", Bitrate: "64 Kbps"},
		{Format: "MP3", Bitrate: "64 Kbps"},
		{Format: "MP3", Bitrate: "Variable"},
	}
	for i := 1; i < len(order); i++ {
		if Quality(&order[i-1]) <= Quality(&order[i]) {
			t.Errorf("%+v should rank above %+v", order[i-1], order[i])
		}
	}
}

func TestListingWithNoAuthorIsNotTreatedAsWrongAuthor(t *testing.T) {
	w := Wanted{Title: "Dune Messiah", Author: "Frank Herbert"}
	exactNoAuthor := ScoreCandidate(w, &Candidate{Heading: "Dune Messiah [2008 recording]", Language: "English"}, "english").Total
	wrongBook := ScoreCandidate(w, &Candidate{Heading: "Dune - Frank Herbert", Language: "English", Format: "FLAC"}, "english").Total
	wrongAuthor := ScoreCandidate(w, &Candidate{Heading: "Dune Messiah - Somebody Else", Language: "English"}, "english").Total
	minScore := DefaultOptions().MinScore
	if exactNoAuthor < minScore || wrongBook >= minScore || wrongAuthor >= minScore {
		t.Errorf("no author %.2f (want >= %.2f), wrong book %.2f and wrong author %.2f (want below)", exactNoAuthor, minScore, wrongBook, wrongAuthor)
	}
}

func TestAudioQualityNeverDecidesBetweenWeakMatches(t *testing.T) {
	src := &fakeSource{
		listings: map[string][]Candidate{"Dune Messiah Herbert": {
			{Heading: "Dune - Frank Herbert", URL: "u/wrong-flac", Language: "English", Format: "FLAC"},
			{Heading: "Dune Messiah - Somebody Else", URL: "u/closer", Language: "English", Format: "MP3", Bitrate: "64 Kbps"},
		}},
		details: map[string]Candidate{"u/wrong-flac": {InfoHash: "a", Authors: []string{"Frank Herbert"}}, "u/closer": {InfoHash: "b", Authors: []string{"Somebody Else"}}},
	}
	r := FindMagnet(Wanted{Title: "Dune Messiah", Author: "Frank Herbert"}, src, DefaultOptions())
	if r.Status != StatusLowConfidence || r.Candidate.URL != "u/closer" {
		t.Errorf("status %s, picked %s (%.2f)", r.Status, r.Candidate.URL, r.Score)
	}
}
