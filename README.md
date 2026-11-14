# audiobook-fetch

Give it a list of audiobooks and a folder. It finds each one, picks the best
edition on its own, and downloads the files into the folder.

```sh
audiobook-fetch books.txt /path/to/folder
```

Searching and downloading are both pluggable. Today it searches
[AudioBook Bay](https://audiobookbay.lu) first, falls back to
[Knaben](https://knaben.org) (a search engine over The Pirate Bay and other
public trackers) for books it cannot match confidently there, and downloads
through
[TorBox](https://torbox.app), so a TorBox account is needed for the download
half. More of either can be added (see [Extending](#extending)).

> **Use this only for material you have the legal right to download.**
> This tool automates searching and downloading; it does not check what you
> are entitled to. That is your responsibility, as is following the terms of
> the services it talks to and the law where you live. It is not affiliated
> with AudioBook Bay, TorBox or any other service.

## Set up

```sh
go build -o bin/audiobook-fetch ./cmd/audiobook-fetch
```

Put your TorBox API key in `~/.config/audiobook-fetch/torbox.key`, or set
`TORBOX_API_KEY`. To avoid typing the folder every time, create
`~/.config/audiobook-fetch/config.json`:

```json
{
  "to": "/path/to/audiobooks",
  "remove_after": true,
  "sources": ["audiobookbay", "knaben"],
  "downloader": "torbox",
  "search_proxy": "",
  "check": true,
  "whisper_model": "/path/to/ggml-base.bin"
}
```

Every key is optional. With `to` set, `audiobook-fetch books.txt` is the
whole command.

## The list

One book per line. Any of these work, and they can be mixed:

```
Title | Author
Title by Author
Title - Author
Just A Title
magnet:?xt=urn:btih:...
# a comment
```

An author makes matches far more reliable. Lines without an explicit `|` are
read as best they can be, and if the first reading finds nothing good the
other readings are tried (so `Author - Title` works too). Bullets and
numbering in front of a line are ignored. A magnet link skips the search and
goes straight to download.

Exports work as well: CSV or TSV with a header row naming a title column
(`Title`, `Author` and similar), and JSON as an array of strings or of
objects with `title` and `author`. Use `-` to read the list from stdin.

## What happens

1. **Find.** Each book is searched for and every listing is scored. The top
   few have their own pages read, then the best is chosen.
2. **Download.** Each chosen link is handed to the downloader. When it has
   the files, they are saved under the folder, a few at a time.
3. **Report.** `results.csv` in the folder's `.audiobook-fetch/` subfolder
   lists every line of the list: what was picked, where from, how confident
   the match was, and whether it downloaded.

Books without a confident match are **not** downloaded; they are listed in
the report for you to check. Add `-include-low` to accept them anyway.

Running the same command again is always safe and picks up where it stopped:
books already matched are not searched again, links already with the
downloader are not added twice, finished files are skipped, and partial files
continue. Add new lines to the list and run again to fetch just those.

## How it picks

Each listing gets a score from 0 to 1:

- **Title, 70%.** Word overlap plus spelling similarity, tolerant of
  subtitles, series prefixes, accents and small typos.
- **Author, 30%.** Surname counts most; initials are ignored. A listing that
  names no author is judged on its title, a little cautiously.
- **Language.** Only English listings are considered (`-lang` changes this).
- **Penalties.** Abridged editions, summaries and study guides,
  dramatizations, bundles, and a volume number that wasn't asked for.
- **Audio quality.** Among listings that match equally well, the best
  sounding wins: bitrate, weighted by format, so M4B beats MP3 at the same
  bitrate.

A pick scoring 0.75 or more counts as a match (`-min-score`).

## Options

`audiobook-fetch -h` lists everything. The ones that matter most:

| Option | Default | |
|--------|---------|--|
| `-remove-after` | off | remove each item from the downloader once its files are here |
| `-cached-only` | off | skip links the downloader would have to fetch first |
| `-include-low` | off | also accept low-confidence matches |
| `-lang NAME` | `english` | wanted language, or `any` |
| `-source NAMES` | `audiobookbay,knaben` | where to search, comma-separated, in the order to try |
| `-downloader NAME` | `torbox` | which service fetches the links |
| `-delay D` | `3s` | pause between requests to a source |
| `-search-proxy URL` | none | send searches through a proxy |
| `-wait D` | `1h` | how long to wait for the downloader to fetch what it lacks |
| `-stall-after D` | `5m` | give up on a link the downloader makes no progress on for this long (`0` keeps waiting) |
| `-parallel N` | `3` | files to download at once |
| `-fresh` | off | search every book again, ignoring earlier matches |
| `-redo` | off | download links again even if an earlier run completed or rejected them |
| `-check` | on | check each download's audio once it is here (`-check=false` turns it off) |
| `-whisper-model FILE` | see below | whisper.cpp model used to listen to samples |
| `-quiet` | off | only print the summaries |

**`-remove-after`** only ever removes items this tool added, and only once
every file of the item is on disk. Anything that was already in your account
is left alone, even if it matches a link in the list.

**Queues.** Some accounts may only fetch a few links at once and queue the
rest. The tool starts queued links itself as slots free up.

**Links with no seeds.** A link nobody is seeding never finishes, and holds
one of those slots while it sits there. Once the downloader has made no
progress on a link for `-stall-after`, the tool gives up on it, reports it as
*no seeds*, and removes it from the downloader (if this tool added it) so the
next link can start. When the link came from a search, the tool then tries
the book's next-best listing, as long as that one also counts as a match and
has nothing against it (an abridged or dramatized edition is never used as a
fallback), and keeps going until one downloads or none are left. Those fallbacks are noted
during the search, so trying them needs no further searching. If a book runs
out of listings that way, the sources its dead listing did not come from are
searched once more before the tool gives up on it.

**Checking what came down.** A listing's description is only a claim, so once
an item's files are here the audio itself is examined:

- *Does it play, and how good is it?* `ffprobe` (part of ffmpeg) reads every
  audio file for its length, bitrate and codec.
- *What language is spoken?* `whisper-cli`
  ([whisper.cpp](https://github.com/ggerganov/whisper.cpp)) listens to three
  half-minute samples spread through the book.
- *Is it the plain, full reading?* File names, tags and the first two minutes
  are searched for words like "abridged", "dramatized" and "full cast".

A download with no audio files, none that play, or most samples in another
language than `-lang` is **rejected**: its files are moved to
`.audiobook-fetch/rejected/` inside the download folder (nothing is deleted),
it is removed from the downloader if this tool added it, and it is not fetched
again on later runs unless `-redo` is given. When the link came from a search,
the book's next-best listing is tried, as for a link with no seeds. Lesser
doubts (low bitrate, some unplayable files, an edition word, one odd language
sample) only **flag** the download; it is kept and the `check:` line says why.

Both tools are optional. Without `ffprobe` nothing is checked; without
`ffmpeg`, `whisper-cli` or a model file the listening checks are skipped, and
the tool says so. The model is `-whisper-model`, else `whisper_model` in the
config, else `WHISPER_MODEL`, else the first `ggml-*.bin` in
`~/.config/audiobook-fetch/models/`.

**Sources.** Each book is searched in the order the sources are given, and a
later source is only asked when the earlier ones gave no near-certain match;
then the best listing across all of them wins.

| Name | What it is |
|------|------------|
| `audiobookbay` | AudioBook Bay: audiobooks only, with author, edition, format and bitrate on each listing |
| `knaben` | Knaben: one API over The Pirate Bay and other public trackers |
| `piratebay` | The Pirate Bay directly; Knaben already covers it, so this is for when Knaben is down |

The general trackers list a release name and little else, so edition, format,
bitrate and language are read from the name where it says. They do report how
many people are seeding, and a listing nobody is seeding is never picked.

**A source refusing you?** Sources limit how fast one address may search.
The tool stops after three failures in a row instead of grinding on; wait and
run again, raise `-delay`, or route searches another way, for example through
an SSH tunnel: `ssh -N -D 1080 somehost` and
`-search-proxy socks5://127.0.0.1:1080`. Downloads are not sent through the
proxy.

## The two halves on their own

```sh
audiobook-fetch find books.txt                  # only find links; writes results/
audiobook-fetch download magnets.txt /folder    # only download links you already have
audiobook-fetch check /folder                   # only check books already downloaded
```

`find` writes `magnets.txt` (one link per confident match), `results.csv` and
`results.json`. `download` takes any file of magnet links. `check` treats each
subfolder as one book and prints a verdict for each (`-json` for machines); it
moves and deletes nothing, and exits non-zero if it would reject any.

## Extending

The work is two stages joined by a magnet link:

```
list ──▶ Source ──▶ best listing ──▶ magnet link ──▶ Downloader ──▶ files
```

Reading the list, scoring, choosing, and saving files resumably are shared.
A source or downloader only has to speak to its own service.

**A new source** is a package under `sources/` implementing three methods:

```go
type Source interface {
    Name() string
    Search(query string, page int) ([]*Candidate, error) // one results page
    Details(c *Candidate) error                           // fill in the rest, incl. the info hash
}
```

`fetch.NewFetcher` gives it paced, retried page fetches (and the proxy
option) for free. `sources/audiobookbay` is the model to copy for a site read
page by page, `sources/knaben` for one with a JSON API. `fetch.ParseRelease`
unpacks a tracker's release name into heading, edition, format, bitrate and
language.

**A new downloader** is a package under `downloaders/` implementing:

```go
type Downloader interface {
    Name() string
    List() ([]Item, error)                                // everything in the account
    CheckCached(hashes []string) (map[string]bool, error) // what it can serve at once
    Add(magnet string) error
    Link(item Item, file File) (string, error)            // a URL to download one file
    Remove(item Item) error
}
```

`downloaders/torbox` is the model. Its API key is read from
`<NAME>_API_KEY` or `~/.config/audiobook-fetch/<name>.key`.

Either one registers itself from `init` with `fetch.RegisterSource` or
`fetch.RegisterDownloader`, and is switched on by adding a blank import to
`cmd/audiobook-fetch/main.go`. Its name then works with `-source` or
`-downloader` and in `config.json`. With several sources, each book is
searched in the order given, and later sources are skipped once an earlier
one yields a near-certain match.

## Use from Go

```go
import (
    fetch "github.com/broots144/audiobook-fetch"
    _ "github.com/broots144/audiobook-fetch/downloaders/torbox"
    _ "github.com/broots144/audiobook-fetch/sources/audiobookbay"
)

source, _ := fetch.NewSource("audiobookbay", fetch.SourceConfig{Delay: 3 * time.Second})
books, links := fetch.ParseList("Frankenstein | Mary Shelley")
result := fetch.Find(books[0], []fetch.Source{source}, fetch.DefaultOptions())
if result.Status == fetch.StatusMatched {
    links = append(links, fetch.Magnet{Hash: result.Candidate.InfoHash, URI: result.Magnet})
}

service, _ := fetch.NewDownloader("torbox", apiKey)
opts := fetch.DefaultDownloadOptions()
opts.Dest = "/path/to/folder"
results, err := fetch.Download(service, links, opts)
```

From any other language, run `find` or `download` with `-json -quiet` and
read stdout.

## Layout

| Path | What |
|------|------|
| `list.go` | reading the list in its various shapes |
| `scoring.go` | how a listing is scored against a wanted book |
| `source.go` | the `Source` interface, its registry, polite page fetching |
| `find.go` | search every source, score, inspect, choose |
| `magnet.go` | building and parsing magnet links |
| `downloader.go` | the `Downloader` interface and its registry |
| `download.go` | links to files on disk, resumable |
| `release.go` | unpacking a tracker's release name |
| `sources/audiobookbay/` | the AudioBook Bay source |
| `sources/knaben/` | the Knaben source |
| `sources/piratebay/` | The Pirate Bay source |
| `downloaders/torbox/` | the TorBox downloader |
| `cmd/audiobook-fetch/` | the command line |

```sh
go test ./...
```

The tests need no network: sources and downloaders are faked.
