package magnetbatch

import (
	"fmt"
	"sort"
	"strings"
)

// The pipeline: wanted book -> search -> score -> check the top few -> magnet.

const (
	StatusMatched       = "matched"
	StatusLowConfidence = "low_confidence"
	StatusNotFound      = "not_found"
	StatusError         = "error"
)

// Source is where listings come from. *Client is the real one.
type Source interface {
	Search(query string, page int) ([]*Candidate, error)
	Details(c *Candidate) error
}

// Options tune how a match is chosen.
type Options struct {
	MinScore    float64 // at or above: a trusted match
	Floor       float64 // below: not worth a details request
	Top         int     // how many front-runners get their details page checked
	CloseMargin float64 // ...but only those within this of the leader
	EarlyStop   float64 // skip broader searches once a hit is this good
	Pages       int     // result pages per search
	Language    string  // only this language is considered; "" means any
	QualityTie  float64 // matches within this of the best compete on audio quality
}

// DefaultOptions are the settings the CLI starts from.
func DefaultOptions() Options {
	return Options{MinScore: 0.75, Floor: 0.4, Top: 4, CloseMargin: 0.2,
		EarlyStop: 0.9, Pages: 1, Language: "english", QualityTie: 0.03}
}

// RunnerUp is a listing that was considered and not chosen.
type RunnerUp struct {
	Score   float64 `json:"score"`
	Heading string  `json:"heading"`
}

// Result is the outcome for one wanted book.
type Result struct {
	Wanted    Wanted     `json:"wanted"`
	Status    string     `json:"status"`
	Score     float64    `json:"score"`
	Breakdown *Score     `json:"breakdown,omitempty"`
	Candidate *Candidate `json:"candidate,omitempty"`
	Magnet    string     `json:"magnet"`
	RunnersUp []RunnerUp `json:"runners_up,omitempty"`
	Note      string     `json:"note"`
}

// ParseList reads one book per line: "Title | Author". The author is
// optional, a tab works in place of the pipe, and lines starting with # are
// comments.
func ParseList(text string) []Wanted {
	var out []Wanted
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\xef\xbb\xbf"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		title, author := line, ""
		for _, sep := range []string{"|", "\t"} {
			if before, after, found := strings.Cut(line, sep); found {
				title, author = before, after
				break
			}
		}
		if title = strings.TrimSpace(title); title != "" {
			out = append(out, Wanted{Title: title, Author: strings.TrimSpace(author), Raw: line})
		}
	}
	return out
}

type scored struct {
	score Score
	cand  *Candidate
}

func byScore(list []scored) {
	sort.SliceStable(list, func(i, j int) bool { return list[i].score.Total > list[j].score.Total })
}

// byScoreThenQuality puts the best matches first and, among trusted matches
// within tie of the best, the best-sounding first.
func byScoreThenQuality(list []scored, tie, minScore float64) {
	if len(list) == 0 {
		return
	}
	byScore(list)
	if list[0].score.Total < minScore {
		return // nothing is a trusted match, so audio quality must not decide
	}
	cutoff := list[0].score.Total - tie
	sort.SliceStable(list, func(i, j int) bool {
		ti, tj := list[i].score.Total >= cutoff, list[j].score.Total >= cutoff
		if ti != tj {
			return ti
		}
		if ti {
			if qi, qj := Quality(list[i].cand), Quality(list[j].cand); qi != qj {
				return qi > qj
			}
		}
		return list[i].score.Total > list[j].score.Total
	})
}

func wrongLanguage(c *Candidate, wanted string) bool {
	return wanted != "" && c.Language != "" && normalize(c.Language) != normalize(wanted)
}

// FindMagnet searches for one book and picks the best listing on its own.
func FindMagnet(w Wanted, src Source, opts Options) Result {
	result, err := find(w, src, opts)
	if err != nil {
		return Result{Wanted: w, Status: StatusError, Note: err.Error()}
	}
	return result
}

func find(w Wanted, src Source, opts Options) (Result, error) {
	var pool []scored
	seen := map[string]bool{}
	best := 0.0
	otherLanguage := 0

	for _, query := range BuildQueries(w) {
		for page := 1; page <= max(1, opts.Pages); page++ {
			hits, err := src.Search(query, page)
			if err != nil {
				return Result{}, err
			}
			for _, c := range hits {
				if seen[c.URL] {
					continue
				}
				seen[c.URL] = true
				if wrongLanguage(c, opts.Language) {
					otherLanguage++
					continue
				}
				s := ScoreCandidate(w, c, opts.Language)
				best = max(best, s.Total)
				pool = append(pool, scored{s, c})
			}
			if len(hits) == 0 {
				break
			}
		}
		if best >= opts.EarlyStop {
			break
		}
	}

	if len(pool) == 0 {
		note := "no search results"
		if otherLanguage > 0 {
			note = fmt.Sprintf("only found in other languages (%d listings)", otherLanguage)
		}
		return Result{Wanted: w, Status: StatusNotFound, Note: note}, nil
	}
	byScoreThenQuality(pool, opts.QualityTie, opts.MinScore)
	leader := pool[0]
	if leader.score.Total < opts.Floor {
		return Result{Wanted: w, Status: StatusNotFound, Score: leader.score.Total,
			Breakdown: &leader.score, Candidate: leader.cand,
			Note: "closest was '" + leader.cand.Heading + "'"}, nil
	}

	// The details page knows things search doesn't (the real author list,
	// abridged or not, the hash), so re-score the front-runners with it
	// before choosing. The pool is already ordered best match first, best
	// audio first among equals, so these are the best-sounding good matches.
	leaderScore := leader.score.Total
	for _, entry := range pool {
		leaderScore = max(leaderScore, entry.score.Total)
	}
	var finalists, others []scored
	for i, entry := range pool {
		if i >= max(1, opts.Top) || entry.score.Total < leaderScore-opts.CloseMargin {
			others = append(others, entry)
			continue
		}
		if err := src.Details(entry.cand); err != nil {
			return Result{}, err
		}
		if entry.cand.InfoHash == "" {
			continue
		}
		finalists = append(finalists, scored{ScoreCandidate(w, entry.cand, opts.Language), entry.cand})
	}
	if len(finalists) == 0 {
		return Result{Wanted: w, Status: StatusNotFound, Score: leader.score.Total,
			Breakdown: &leader.score, Candidate: leader.cand,
			Note: "best matches had no hash on their page"}, nil
	}
	byScoreThenQuality(finalists, opts.QualityTie, opts.MinScore)
	winner := finalists[0]

	rest := append(finalists[1:], others...)
	byScore(rest)
	var runnersUp []RunnerUp
	for _, r := range rest[:min(3, len(rest))] {
		runnersUp = append(runnersUp, RunnerUp{r.score.Total, r.cand.Heading})
	}

	status := StatusMatched
	if winner.score.Total < opts.MinScore {
		status = StatusLowConfidence
	}
	return Result{
		Wanted:    w,
		Status:    status,
		Score:     winner.score.Total,
		Breakdown: &winner.score,
		Candidate: winner.cand,
		Magnet:    BuildMagnet(winner.cand.InfoHash, winner.cand.Heading, winner.cand.Trackers),
		RunnersUp: runnersUp,
		Note:      strings.Join(winner.score.Notes, "; "),
	}, nil
}

// FindMagnets runs FindMagnet over a list, calling onResult (if not nil)
// after each one.
func FindMagnets(list []Wanted, src Source, opts Options, onResult func(int, Result)) []Result {
	results := make([]Result, 0, len(list))
	for i, w := range list {
		r := FindMagnet(w, src, opts)
		if onResult != nil {
			onResult(i, r)
		}
		results = append(results, r)
	}
	return results
}
