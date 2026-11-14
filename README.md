# magnet-batch

Give it a text file of books, get back a magnet link for the best match of each.
It searches, scores every listing, inspects the front-runners and picks one on
its own. Nothing to choose by hand.

## Use

```sh
go build -o bin/magnet-batch ./cmd/magnet-batch
./bin/magnet-batch books.txt
```

`books.txt` is one book per line (see `books.example.txt`):

```
Title | Author
Title With No Author
# comments start with a hash
```

Output goes to `magnet-results/`:

| File | What |
|------|------|
| `magnets.txt` | one magnet link per confident match, ready to paste into a client |
| `results.csv` | every input line: status, score, what was picked, edition, format, size, runners-up, magnet |
| `results.json` | the same, with the full score breakdown |

Flags go before the file name:

| Flag | Default | |
|------|---------|--|
| `-out DIR` | `magnet-results` | where the files go |
| `-min-score N` | `0.75` | score a pick needs to count as a match |
| `-include-low` | off | also put low-confidence picks in `magnets.txt` |
| `-resume` | off | keep matches from the last run, only look up the rest |
| `-lang NAME` | `english` | wanted language, or `any` |
| `-top N` | `3` | front-runners to inspect in detail before choosing |
| `-pages N` | `1` | result pages to read per search |
| `-delay D` | `3s` | pause between requests |
| `-json` | off | print full results as JSON on stdout |
| `-quiet` | off | no per-book progress |
| `-base-url URL` | built in | site to search (or `MAGNET_BATCH_BASE_URL`) |

Use `-` as the file name to read the list from stdin.

## How it picks

Each listing gets a score from 0 to 1:

- **Title, 70%.** Word overlap plus spelling similarity, tolerant of subtitles,
  series prefixes, accents and small typos.
- **Author, 30%.** Surname counts most; initials are ignored. With no author on
  the line, the title is the whole score.
- **Penalties.** Wrong language, abridged, summaries and study guides,
  dramatizations, bundles, and a volume number that wasn't asked for.
- **Tie-break.** A small bonus for M4B and a decent bitrate.

The top few listings then have their own pages read, which adds the real author
list and whether the edition is abridged, and they are scored again before the
winner is chosen.

Every line ends up as one of:

- `matched`: score at or above `-min-score`; link is in `magnets.txt`
- `low_confidence`: a best guess below it; link is in the report only
- `not_found`: nothing came close
- `error`: the site could not be reached for this one

## Use from Go

```go
import mb "magnetbatch"

client := mb.NewClient()
result := mb.FindMagnet(mb.Wanted{Title: "Frankenstein", Author: "Mary Shelley"}, client, mb.DefaultOptions())
if result.Status == mb.StatusMatched {
    fmt.Println(result.Magnet)
}
```

`mb.ParseList` reads the text format and `mb.FindMagnets` runs a whole list.
From any other language, run the binary with `-json -quiet` and read stdout.

## Develop

```sh
go test ./...
```

The scoring rules live in `scoring.go` and are covered by tests that need no
network. Tune the weights there.
