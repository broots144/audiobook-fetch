package audiobookfetch

import (
	"fmt"
	"sort"
	"strings"
)

// Finding: wanted book -> search every source -> score -> inspect the top
// few -> the best listing's magnet link.

const (
	StatusMatched       = "matched"
	StatusLowConfidence = "low_confidence"
	StatusNotFound      = "not_found"
	StatusError         = "error"
)

// Options tune how a match is chosen.
type Options struct {
	MinScore    float64 // at or above: a trusted match
	Floor       float64 // below: not worth a details request
	Top         int     // how many front-runners get their details page checked
	CloseMargin float64 // ...but only those within this of the leader
	EarlyStop   float64 // skip broader searches and further sources once a hit is this good
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

// Alternate is another listing of the same book that also passed as a match,
// with no penalty against it (so never an abridged or dramatized edition):
// the one to turn to when the chosen listing cannot be downloaded.
type Alternate struct {
	Score     float64    `json:"score"`
	Breakdown *Score     `json:"breakdown,omitempty"`
	Candidate *Candidate `json:"candidate"`
	Magnet    string     `json:"magnet"`
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
	// Alternates are the other trusted matches, next best first. They cost
	// nothing extra to find: their pages were read while choosing.
	Alternates []Alternate `json:"alternates,omitempty"`
	Note       string      `json:"note"`
}

// NextBest replaces the chosen listing with the best alternate, for when the
// chosen one turned out to be undownloadable (why says how). It reports
// false, changing nothing, when there is no alternate left.
func (r *Result) NextBest(why string) bool {
	if len(r.Alternates) == 0 {
		return false
	}
	next := r.Alternates[0]
	r.Alternates = r.Alternates[1:]
	r.Score, r.Breakdown, r.Candidate, r.Magnet = next.Score, next.Breakdown, next.Candidate, next.Magnet
	r.Note = strings.TrimSuffix("next-best listing: "+why+"; "+r.Note, "; ")
	return true
}

// scored is a candidate with its score and the source that can tell us more
// about it.
type scored struct {
	score Score
	cand  *Candidate
	from  Source
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

// Find searches the sources for one book and picks the best listing on its
// own. Sources are tried in the order given; once one yields a near-certain
// match the rest are not asked.
func Find(w Wanted, sources []Source, opts Options) Result {
	result, err := find(w, sources, opts)
	if err != nil {
		return Result{Wanted: w, Status: StatusError, Note: err.Error()}
	}
	if result.Status == StatusMatched {
		return result
	}
	// The line had no explicit separator, so its title and author were a
	// guess. Before settling for a weak match, try the other readings.
	for _, reading := range alternates(w) {
		other, err := find(reading, sources, opts)
		if err != nil || other.Score <= result.Score {
			continue
		}
		read := "read as title '" + reading.Title + "'"
		if reading.Author != "" {
			read += ", author '" + reading.Author + "'"
		}
		other.Wanted = w
		other.Note = strings.TrimSuffix(read+"; "+other.Note, "; ")
		result = other
		if result.Status == StatusMatched {
			break
		}
	}
	return result
}

// search gathers and scores candidates from one source into pool.
func search(w Wanted, src Source, opts Options, seen map[string]bool, pool *[]scored, otherLanguage *int) (best float64, err error) {
	for _, query := range BuildQueries(w) {
		for page := 1; page <= max(1, opts.Pages); page++ {
			hits, err := src.Search(query, page)
			if err != nil {
				return best, err
			}
			for _, c := range hits {
				if seen[c.URL] {
					continue
				}
				seen[c.URL] = true
				c.Source = src.Name()
				if wrongLanguage(c, opts.Language) {
					*otherLanguage++
					continue
				}
				s := ScoreCandidate(w, c, opts.Language)
				best = max(best, s.Total)
				*pool = append(*pool, scored{s, c, src})
			}
			if len(hits) == 0 {
				break
			}
		}
		if best >= opts.EarlyStop {
			break
		}
	}
	return best, nil
}

func find(w Wanted, sources []Source, opts Options) (Result, error) {
	var pool []scored
	seen := map[string]bool{}
	otherLanguage := 0

	// One source failing is not fatal while another can still answer.
	var lastErr error
	for _, src := range sources {
		best, err := search(w, src, opts, seen, &pool, &otherLanguage)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", src.Name(), err)
			continue
		}
		if best >= opts.EarlyStop {
			break
		}
	}
	if len(pool) == 0 {
		if lastErr != nil {
			return Result{}, lastErr
		}
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

	// A listing's own page knows things its search result doesn't (the real
	// author list, abridged or not, the hash), so re-score the front-runners
	// with it before choosing. The pool is already ordered best match first,
	// best audio first among equals, so these are the best-sounding good
	// matches.
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
		if err := entry.from.Details(entry.cand); err != nil {
			lastErr = fmt.Errorf("%s: %w", entry.from.Name(), err)
			continue
		}
		if entry.cand.InfoHash == "" {
			continue
		}
		finalists = append(finalists, scored{ScoreCandidate(w, entry.cand, opts.Language), entry.cand, entry.from})
	}
	if len(finalists) == 0 {
		if lastErr != nil {
			return Result{}, lastErr
		}
		return Result{Wanted: w, Status: StatusNotFound, Score: leader.score.Total,
			Breakdown: &leader.score, Candidate: leader.cand,
			Note: "best matches had no hash on their page"}, nil
	}
	byScoreThenQuality(finalists, opts.QualityTie, opts.MinScore)
	winner := finalists[0]

	// Other listings that are just as surely this book, kept as fallbacks.
	// A fallback is used without anyone looking, so it must be a clean
	// match: nothing abridged, dramatized, bundled or otherwise penalised,
	// even where such a listing would still clear MinScore.
	var alternates []Alternate
	offered := map[string]bool{winner.cand.InfoHash: true}
	for _, f := range finalists[1:] {
		if f.score.Total < opts.MinScore || f.score.Penalty > 0 || offered[f.cand.InfoHash] {
			continue
		}
		offered[f.cand.InfoHash] = true
		score := f.score
		alternates = append(alternates, Alternate{score.Total, &score, f.cand,
			BuildMagnet(f.cand.InfoHash, f.cand.Heading, f.cand.Trackers)})
	}

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
		Wanted:     w,
		Status:     status,
		Score:      winner.score.Total,
		Breakdown:  &winner.score,
		Candidate:  winner.cand,
		Magnet:     BuildMagnet(winner.cand.InfoHash, winner.cand.Heading, winner.cand.Trackers),
		RunnersUp:  runnersUp,
		Alternates: alternates,
		Note:       strings.Join(winner.score.Notes, "; "),
	}, nil
}
