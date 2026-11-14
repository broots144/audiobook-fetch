package audiobookfetch

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

func TestParseListAcceptsManyShapes(t *testing.T) {
	type book struct{ title, author string }
	want := []book{{"Dune", "Frank Herbert"}, {"The Hobbit", "J.R.R. Tolkien"}, {"No Author Here", ""}}
	lists := map[string]string{
		"pipes":         "# my books\n\nDune | Frank Herbert\n  The Hobbit|J.R.R. Tolkien  \nNo Author Here\n | orphan author\n",
		"pipes+heading": "Title | Author\nDune | Frank Herbert\nThe Hobbit | J.R.R. Tolkien\nNo Author Here\n",
		"tabs":          "Dune\tFrank Herbert\nThe Hobbit\tJ.R.R. Tolkien\nNo Author Here\n",
		"by":            "1. Dune by Frank Herbert\n2. The Hobbit by J.R.R. Tolkien\n3. No Author Here\n",
		"dashes":        "- Dune - Frank Herbert\n- The Hobbit – J.R.R. Tolkien\n- No Author Here\n",
		"csv":           "Title,Author,Year\nDune,Frank Herbert,1965\n\"The Hobbit\",\"J.R.R. Tolkien\",1937\nNo Author Here,,\n",
		"csv reordered": "Rating,Authors,Book Title\n5,Frank Herbert,Dune\n4,J.R.R. Tolkien,The Hobbit\n3,,No Author Here\n",
		"tsv":           "title\tauthor\nDune\tFrank Herbert\nThe Hobbit\tJ.R.R. Tolkien\nNo Author Here\t\n",
		"json objects":  `[{"title":"Dune","author":"Frank Herbert"},{"Title":"The Hobbit","Authors":["J.R.R. Tolkien"]},{"name":"No Author Here"}]`,
		"json strings":  `["Dune | Frank Herbert", "The Hobbit by J.R.R. Tolkien", "No Author Here"]`,
		"json wrapped":  `{"books":[{"title":"Dune","author":{"name":"Frank Herbert"}},{"title":"The Hobbit","author":"J.R.R. Tolkien"},{"title":"No Author Here"}]}`,
	}
	for name, text := range lists {
		books, magnets := ParseList(text)
		if len(magnets) != 0 || len(books) != len(want) {
			t.Errorf("%s: got %d books and %d magnets: %+v", name, len(books), len(magnets), books)
			continue
		}
		for i, w := range want {
			if books[i].Title != w.title || books[i].Author != w.author {
				t.Errorf("%s: book %d = %q | %q, want %q | %q", name, i, books[i].Title, books[i].Author, w.title, w.author)
			}
		}
	}
}

func TestParseListKeepsTrickyTitlesAndMagnets(t *testing.T) {
	text := "1984\n2001: A Space Odyssey | Arthur C. Clarke\nStand by Me 2: The Return\n" +
		"magnet:?xt=urn:btih:" + hashA + "&dn=x\nmagnet:?xt=urn:btih:" + hashA + "&dn=again\nCatch-22 | Joseph Heller\n"
	books, magnets := ParseList(text)
	titles := []string{"1984", "2001: A Space Odyssey", "Stand by Me 2: The Return", "Catch-22"}
	if len(books) != len(titles) || len(magnets) != 1 || magnets[0].Hash != hashA {
		t.Fatalf("books %+v, magnets %+v", books, magnets)
	}
	for i, title := range titles {
		if books[i].Title != title || books[i].Guess != "" {
			t.Errorf("book %d = %+v, want title %q read literally", i, books[i], title)
		}
	}
}

func TestGuessedLinesAreRetriedAnotherWay(t *testing.T) {
	// "Author - Title" order: the first reading finds nothing good, the swap does.
	src := &fakeSource{
		listings: map[string][]Candidate{"Dune Herbert": {{Heading: "Dune - Frank Herbert", URL: "u/1", Language: "English"}}},
		details:  map[string]Candidate{"u/1": {Authors: []string{"Frank Herbert"}, InfoHash: "abc"}},
	}
	books, _ := ParseList("Frank Herbert - Dune\n")
	r := Find(books[0], []Source{src}, DefaultOptions())
	if r.Status != StatusMatched || r.Wanted.Raw != "Frank Herbert - Dune" || !strings.Contains(r.Note, "read as title 'Dune'") {
		t.Errorf("status %s (%.2f), wanted %+v, note %q", r.Status, r.Score, r.Wanted, r.Note)
	}

	// "Title by Something" where the whole line is the title.
	src = &fakeSource{
		listings: map[string][]Candidate{"Stand by Me": {{Heading: "Stand by Me - Some Writer", URL: "u/2", Language: "English"}}},
		details:  map[string]Candidate{"u/2": {Authors: []string{"Some Writer"}, InfoHash: "def"}},
	}
	books, _ = ParseList("Stand by Me\n")
	if r = Find(books[0], []Source{src}, DefaultOptions()); r.Status != StatusMatched {
		t.Errorf("status %s (%.2f), note %q, searches %v", r.Status, r.Score, r.Note, src.searches)
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

// fakeSource answers searches from a fixed table and applies canned details.
type fakeSource struct {
	name     string
	listings map[string][]Candidate // query -> listings
	details  map[string]Candidate   // url -> fields the details page adds
	searches []string
	fetched  []string
	failWith error
}

func (f *fakeSource) Name() string {
	if f.name == "" {
		return "fake"
	}
	return f.name
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
	r := Find(Wanted{Title: "Dune", Author: "Frank Herbert"}, []Source{src}, DefaultOptions())

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
	r := Find(Wanted{Title: "Dune", Author: "Frank Herbert"}, []Source{src}, DefaultOptions())
	if r.Status != StatusMatched || strings.Join(src.searches, "|") != "Dune Herbert|Dune" {
		t.Errorf("status %s (%.2f), searches %v", r.Status, r.Score, src.searches)
	}
}

func TestFindMagnetOutcomesWithoutAGoodMatch(t *testing.T) {
	w := Wanted{Title: "Dune", Author: "Frank Herbert"}

	empty := Find(w, []Source{&fakeSource{}}, DefaultOptions())
	if empty.Status != StatusNotFound || empty.Magnet != "" {
		t.Errorf("no results: %+v", empty)
	}

	unrelated := &fakeSource{listings: map[string][]Candidate{"Dune": {{Heading: "Gardening Basics - A. Person", URL: "u/x"}}}}
	r := Find(w, []Source{unrelated}, DefaultOptions())
	if r.Status != StatusNotFound || r.Magnet != "" || len(unrelated.fetched) != 0 {
		t.Errorf("unrelated result: status %s, magnet %q, fetched %v", r.Status, r.Magnet, unrelated.fetched)
	}

	sequelOnly := &fakeSource{
		listings: map[string][]Candidate{"Dune Herbert": {{Heading: "Dune Messiah - Frank Herbert", URL: "u/s", Language: "English"}}},
		details:  map[string]Candidate{"u/s": {Authors: []string{"Frank Herbert"}, InfoHash: "ddd"}},
	}
	r = Find(w, []Source{sequelOnly}, DefaultOptions())
	if r.Status != StatusLowConfidence || r.Magnet == "" {
		t.Errorf("sequel only: status %s (%.2f), magnet %q", r.Status, r.Score, r.Magnet)
	}

	noHash := &fakeSource{listings: map[string][]Candidate{"Dune Herbert": {{Heading: "Dune - Frank Herbert", URL: "u/n"}}}}
	if r = Find(w, []Source{noHash}, DefaultOptions()); r.Status != StatusNotFound {
		t.Errorf("no hash: status %s", r.Status)
	}

	broken := Find(w, []Source{&fakeSource{failWith: errors.New("HTTP 503")}}, DefaultOptions())
	if broken.Status != StatusError || broken.Note != "fake: HTTP 503" {
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
	r := Find(Wanted{Title: "Dune", Author: "Frank Herbert"}, []Source{src}, DefaultOptions())
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
	r := Find(w, []Source{src}, DefaultOptions())
	if r.Status != StatusNotFound || r.Magnet != "" || len(src.fetched) != 0 || !strings.Contains(r.Note, "other languages") {
		t.Errorf("english only: %+v, fetched %v", r, src.fetched)
	}

	any := DefaultOptions()
	any.Language = ""
	src.details = map[string]Candidate{"u/de": {InfoHash: "a"}, "u/es": {InfoHash: "b"}}
	if r = Find(w, []Source{src}, any); r.Status != StatusMatched {
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
	r := Find(Wanted{Title: "Dune Messiah", Author: "Frank Herbert"}, []Source{src}, DefaultOptions())
	if r.Status != StatusLowConfidence || r.Candidate.URL != "u/closer" {
		t.Errorf("status %s, picked %s (%.2f)", r.Status, r.Candidate.URL, r.Score)
	}
}

func TestFindUsesEverySourceAndSurvivesOneFailing(t *testing.T) {
	w := Wanted{Title: "Dune", Author: "Frank Herbert"}
	weak := &fakeSource{name: "weak", listings: map[string][]Candidate{"Dune Herbert": {
		{Heading: "Dune Messiah - Frank Herbert", URL: "weak/1", Language: "English"}}},
		details: map[string]Candidate{"weak/1": {InfoHash: "aaa"}}}
	broken := &fakeSource{name: "broken", failWith: errors.New("HTTP 503")}
	good := &fakeSource{name: "good", listings: map[string][]Candidate{"Dune Herbert": {
		{Heading: "Dune - Frank Herbert", URL: "good/1", Language: "English"}}},
		details: map[string]Candidate{"good/1": {Authors: []string{"Frank Herbert"}, InfoHash: "bbb"}}}

	r := Find(w, []Source{weak, broken, good}, DefaultOptions())
	if r.Status != StatusMatched || r.Candidate.URL != "good/1" || r.Candidate.Source != "good" {
		t.Fatalf("status %s, picked %+v", r.Status, r.Candidate)
	}
	if strings.Join(good.fetched, ",") != "good/1" {
		t.Errorf("details must come from the source that listed the candidate: %v", good.fetched)
	}

	// A near-certain match from the first source means the rest are not asked.
	never := &fakeSource{name: "never"}
	if r = Find(w, []Source{good, never}, DefaultOptions()); r.Status != StatusMatched || len(never.searches) != 0 {
		t.Errorf("status %s; second source was searched %v", r.Status, never.searches)
	}

	// When every source fails, that is an error rather than "not found".
	if r = Find(w, []Source{broken}, DefaultOptions()); r.Status != StatusError || !strings.Contains(r.Note, "broken: HTTP 503") {
		t.Errorf("all sources failing: %+v", r)
	}
}

func TestRegistries(t *testing.T) {
	RegisterSource("test-source", func(SourceConfig) (Source, error) { return &fakeSource{name: "registered"}, nil })
	src, err := NewSource("test-source", SourceConfig{})
	if err != nil || src.Name() != "registered" {
		t.Errorf("source %v, err %v", src, err)
	}
	if _, err := NewSource("nope", SourceConfig{}); err == nil || !strings.Contains(err.Error(), "test-source") {
		t.Errorf("unknown source should list what exists, got %v", err)
	}
	if _, err := NewDownloader("nope", "key"); err == nil || !strings.Contains(err.Error(), "unknown downloader") {
		t.Errorf("unknown downloader: %v", err)
	}
	if _, err := NewFetcher(SourceConfig{Proxy: "not a url"}); err == nil {
		t.Error("a bad proxy address should be refused")
	}
}

func TestOtherTrustedListingsAreKeptAsFallbacks(t *testing.T) {
	src := &fakeSource{
		listings: map[string][]Candidate{"Dune Herbert": {
			{Heading: "Dune - Frank Herbert", URL: "u/best", Language: "English", Format: "M4B", Bitrate: "128 Kbps"},
			{Heading: "Dune - Frank Herbert", URL: "u/second", Language: "English", Format: "MP3", Bitrate: "64 Kbps"},
			{Heading: "Dune - Frank Herbert", URL: "u/same", Language: "English", Format: "MP3", Bitrate: "32 Kbps"},
			{Heading: "Dune (BBC Dramatization) - Frank Herbert", URL: "u/drama", Language: "English"},
		}},
		details: map[string]Candidate{
			"u/best":   {Authors: []string{"Frank Herbert"}, InfoHash: "aaa"},
			"u/second": {Authors: []string{"Frank Herbert"}, InfoHash: "bbb"},
			"u/same":   {Authors: []string{"Frank Herbert"}, InfoHash: "aaa"}, // the winner's files, listed twice
			"u/drama":  {Authors: []string{"Frank Herbert"}, InfoHash: "ccc"}, // not a trusted match
		},
	}
	r := Find(Wanted{Title: "Dune", Author: "Frank Herbert"}, []Source{src}, DefaultOptions())

	if r.Candidate.URL != "u/best" || len(r.Alternates) != 1 || r.Alternates[0].Candidate.URL != "u/second" {
		t.Fatalf("picked %s with alternates %+v; want only the other trusted, different listing", r.Candidate.URL, r.Alternates)
	}
	if !r.NextBest("the first had no seeds") {
		t.Fatal("there was an alternate to move to")
	}
	if r.Candidate.URL != "u/second" || !strings.Contains(r.Magnet, "btih:bbb") || !strings.Contains(r.Note, "no seeds") {
		t.Errorf("after moving on: %s, %s, note %q", r.Candidate.URL, r.Magnet, r.Note)
	}
	if r.NextBest("again") || r.Candidate.URL != "u/second" {
		t.Error("with no alternate left, the result must stay as it is")
	}
}
