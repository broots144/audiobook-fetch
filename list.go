package audiobookfetch

import (
	"encoding/csv"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

// Reading the list of wanted books. Several shapes are accepted so that a
// list can be pasted or exported from wherever it lives:
//
//	Title | Author            (a tab works too; the author is optional)
//	Title by Author
//	Title - Author            (or Author - Title; both are tried)
//	magnet:?xt=urn:btih:...   (already a link: goes straight to download)
//	CSV or TSV with a header row naming a title column
//	JSON: an array of strings, or of objects with title and author
//
// Blank lines and lines starting with # are skipped, as are list bullets
// and numbering in front of a line.

// How a line with no explicit separator was read. When the first reading
// finds no confident match, the other readings are tried (see alternates).
const (
	GuessBy   = "by"   // "Title by Author"
	GuessDash = "dash" // "Title - Author"
)

var (
	bullet       = regexp.MustCompile(`^(?:[-*•]\s+|\d+[.)]\s+)`)
	bySeparator  = regexp.MustCompile(`(?i)\s+by\s+`)
	titleColumns = map[string]bool{"title": true, "booktitle": true, "name": true, "book": true}
	authorColumn = map[string]bool{"author": true, "authors": true, "authorname": true, "by": true, "creator": true, "writtenby": true}
)

// ParseList reads a list in any of the accepted shapes. Lines that are
// magnet links are returned separately, since they need no search.
func ParseList(text string) (books []Wanted, magnets []Magnet) {
	text = strings.TrimPrefix(text, "\xef\xbb\xbf")
	if trimmed := strings.TrimSpace(text); strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") {
		if books, ok := parseJSONList(trimmed); ok {
			return books, nil
		}
	}
	if books, ok := parseTable(text); ok {
		return books, nil
	}
	return parseLines(text)
}

func newWanted(title, author string) (Wanted, bool) {
	title, author = strings.TrimSpace(title), strings.TrimSpace(author)
	if title == "" {
		return Wanted{}, false
	}
	raw := title
	if author != "" {
		raw = title + " | " + author
	}
	return Wanted{Title: title, Author: author, Raw: raw}, true
}

// columnKey reduces a header or JSON key to lowercase letters: "Book Title" -> "booktitle".
func columnKey(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, name)
}

func parseLines(text string) (books []Wanted, magnets []Magnet) {
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if found, _ := ParseMagnets(line); len(found) == 1 {
			if !seen[found[0].Hash] {
				seen[found[0].Hash] = true
				magnets = append(magnets, found[0])
			}
			continue
		}
		w, ok := parseLine(line)
		if !ok {
			continue
		}
		if len(books) == 0 && titleColumns[columnKey(w.Title)] && (w.Author == "" || authorColumn[columnKey(w.Author)]) {
			continue // a "Title | Author" heading, not a book
		}
		books = append(books, w)
	}
	return books, magnets
}

// parseLine reads one "Title | Author"-style line.
func parseLine(line string) (Wanted, bool) {
	line = strings.TrimSpace(bullet.ReplaceAllString(strings.TrimSpace(line), ""))
	line = strings.Trim(line, `"“”`)
	for _, sep := range []string{"|", "\t"} {
		if title, author, found := strings.Cut(line, sep); found {
			w, ok := newWanted(title, author)
			w.Raw = line
			return w, ok
		}
	}
	if at := bySeparator.FindAllStringIndex(line, -1); len(at) > 0 {
		last := at[len(at)-1]
		if title, author := line[:last[0]], line[last[1]:]; looksLikeName(author) {
			return Wanted{Title: strings.TrimSpace(title), Author: strings.TrimSpace(author), Raw: line, Guess: GuessBy}, true
		}
	}
	if at := headingSplit.FindAllStringIndex(line, -1); len(at) > 0 {
		last := at[len(at)-1]
		if title, author := strings.TrimSpace(line[:last[0]]), strings.TrimSpace(line[last[1]:]); title != "" && author != "" {
			return Wanted{Title: title, Author: author, Raw: line, Guess: GuessDash}, true
		}
	}
	w, ok := newWanted(line, "")
	return w, ok
}

// looksLikeName is a cheap check that the text after "by" is a person, so
// that "Stand by Me" is not always read as a book called "Stand".
func looksLikeName(text string) bool {
	words := strings.Fields(text)
	if len(words) == 0 || len(words) > 5 {
		return false
	}
	return !strings.ContainsAny(text, "0123456789:")
}

// alternates returns the other ways a guessed line could be read.
func alternates(w Wanted) []Wanted {
	switch w.Guess {
	case GuessBy:
		return []Wanted{{Title: w.Raw, Raw: w.Raw}}
	case GuessDash:
		return []Wanted{{Title: w.Author, Author: w.Title, Raw: w.Raw}, {Title: w.Raw, Raw: w.Raw}}
	}
	return nil
}

// parseTable reads CSV or TSV whose first row names a title column.
func parseTable(text string) ([]Wanted, bool) {
	var header string
	rest := text
	for rest != "" {
		line, after, _ := strings.Cut(rest, "\n")
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			header = line
			break
		}
		rest = after
	}
	if header == "" || strings.Contains(header, "|") {
		return nil, false
	}
	delimiter := ','
	if strings.Contains(header, "\t") {
		delimiter = '\t'
	}
	reader := csv.NewReader(strings.NewReader(rest))
	reader.Comma, reader.LazyQuotes, reader.FieldsPerRecord, reader.TrimLeadingSpace = delimiter, true, -1, true
	rows, err := reader.ReadAll()
	if err != nil || len(rows) == 0 {
		return nil, false
	}
	titleAt, authorAt := -1, -1
	for i, name := range rows[0] {
		switch key := columnKey(name); {
		case titleColumns[key] && titleAt < 0:
			titleAt = i
		case authorColumn[key] && authorAt < 0:
			authorAt = i
		}
	}
	if titleAt < 0 || (len(rows[0]) > 1 && authorAt < 0 && delimiter == ',' && len(rows[0]) == 2) {
		return nil, false // no title column, or "Title, The"-style text rather than a header
	}
	var books []Wanted
	for _, row := range rows[1:] {
		if titleAt >= len(row) {
			continue
		}
		author := ""
		if authorAt >= 0 && authorAt < len(row) {
			author = row[authorAt]
		}
		if w, ok := newWanted(row[titleAt], author); ok {
			books = append(books, w)
		}
	}
	return books, true
}

// parseJSONList reads an array of strings or objects, or an object wrapping one.
func parseJSONList(text string) ([]Wanted, bool) {
	var doc any
	if json.Unmarshal([]byte(text), &doc) != nil {
		return nil, false
	}
	if object, ok := doc.(map[string]any); ok {
		for _, value := range object {
			if _, isList := value.([]any); isList {
				doc = value
				break
			}
		}
	}
	items, ok := doc.([]any)
	if !ok {
		return nil, false
	}
	var books []Wanted
	for _, item := range items {
		switch v := item.(type) {
		case string:
			if w, ok := parseLine(v); ok {
				books = append(books, w)
			}
		case map[string]any:
			var title, author string
			for name, value := range v {
				switch key := columnKey(name); {
				case titleColumns[key] && title == "":
					title, _ = value.(string)
				case authorColumn[key] && author == "":
					author = jsonNames(value)
				}
			}
			if w, ok := newWanted(title, author); ok {
				books = append(books, w)
			}
		}
	}
	return books, true
}

// jsonNames flattens an author value: a string, a list of strings, or a
// list of objects with a name.
func jsonNames(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case map[string]any:
		name, _ := v["name"].(string)
		return name
	case []any:
		var names []string
		for _, item := range v {
			if name := jsonNames(item); name != "" {
				names = append(names, name)
			}
		}
		return strings.Join(names, ", ")
	}
	return ""
}
