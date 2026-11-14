# audiobook-fetch

[![CI](https://github.com/broots144/audiobook-fetch/actions/workflows/ci.yml/badge.svg)](https://github.com/broots144/audiobook-fetch/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Give it a list of audiobooks and a folder. It finds each one, picks the best
edition on its own, downloads the files into the folder, unpacks them if they
came as an archive, and checks that what arrived really is that book.

```sh
audiobook-fetch books.txt /path/to/folder
```

> [!IMPORTANT]
> **Only use this for audiobooks you are legally allowed to acquire**: works
> in the public domain, works whose rights holder permits free distribution,
> or anything else you have the right to download where you live.
>
> The tool automates searching and downloading. It cannot tell what you are
> entitled to, and it does not try to. That is your responsibility, as is
> following the law where you live and the terms of every service it talks
> to. The authors do not condone copyright infringement and accept no
> liability for how the tool is used (see [LICENSE](LICENSE)). It is not
> affiliated with any site or service it can search or download through.

**Built from plugins.** Where it searches (a *source*) and what fetches the
links (a *downloader*) are both small plugins behind a Go interface. Three
sources and one downloader ship with it, you choose between them with
`-source` and `-downloader`, and adding your own is one package: see
[Extending](#extending).

## Quick start

1. **Build it** (Go 1.27 or newer):

   ```sh
   go install github.com/broots144/audiobook-fetch/cmd/audiobook-fetch@latest
   ```

2. **Give it a downloader key.** Downloads go through
   [TorBox](https://torbox.app), so you need an account there. Put its API
   key in `~/.config/audiobook-fetch/torbox.key` and `chmod 600` it, or set
   `TORBOX_API_KEY`.

3. **Write a list**, one book per line (see [`books.example.txt`](books.example.txt)):

   ```
   Pride and Prejudice | Jane Austen
   Frankenstein | Mary Shelley
   ```

4. **Run it:**

   ```sh
   audiobook-fetch books.txt /path/to/folder
   ```

   Books with a confident match are downloaded. The rest are listed in
   `/path/to/folder/.audiobook-fetch/results.csv` for you to look at. Running
   the same command again is always safe: it picks up where it stopped.

**Optional, recommended:** install `ffmpeg` and `whisper-cpp` (both are in
Homebrew and most package managers) and put a whisper.cpp model such as
`ggml-base.bin` in `~/.config/audiobook-fetch/models/`. Each download's
audio is then [checked](#checking-what-came-down): playable, spoken language,
abridged or dramatized. Zip archives are unpacked without anything extra; for
rar and 7z, install one of `7zz`, `7z`, `unar` or `bsdtar` (macOS has
`bsdtar` already).

## Contents

- [The list](#the-list): what a line can look like
- [What happens](#what-happens) and [how it picks](#how-it-picks)
- [Options](#options) and the [config file](#config-file)
- [Behaviour in detail](#behaviour-in-detail): queues, dead links, archives, the audio check, duplicate formats, sources
- [The parts on their own](#the-parts-on-their-own): `find`, `download`, `check`, `tidy`
- [Extending](#extending): the source and downloader plugins, and [use from Go](#use-from-go)
- [Contributing](CONTRIBUTING.md), [security](SECURITY.md), [license](#license)

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
| `-unpack` | on | unpack zip, rar and 7z archives once downloaded, then delete them |
| `-keep-one-format` | on | when a download holds the same book in several formats, keep the best and move the rest aside |
| `-quiet` | off | only print the summaries |

### Config file

To avoid typing the folder and options every time, create
`~/.config/audiobook-fetch/config.json`:

```json
{
  "to": "/path/to/audiobooks",
  "remove_after": true,
  "sources": ["audiobookbay", "knaben"],
  "downloader": "torbox",
  "search_proxy": "",
  "check": true,
  "whisper_model": "/path/to/ggml-base.bin",
  "unpack": true,
  "keep_one_format": true
}
```

Every key is optional. With `to` set, `audiobook-fetch books.txt` is the
whole command.

## Behaviour in detail

### Removing from the downloader

**`-remove-after`** only ever removes items this tool added, and only once
every file of the item is on disk. Anything that was already in your account
is left alone, even if it matches a link in the list.

### Queues

Some accounts may only fetch a few links at once and queue the
rest. The tool starts queued links itself as slots free up.

### Links with no seeds

A link nobody is seeding never finishes, and holds
one of those slots while it sits there. Once the downloader has made no
progress on a link for `-stall-after`, the tool gives up on it, reports it as
*no seeds*, and removes it from the downloader (if this tool added it) so the
next link can start. When the link came from a search, the tool then tries
the book's next-best listing, as long as that one also counts as a match and
has nothing against it (an abridged or dramatized edition is never used as a
fallback), and keeps going until one downloads or none are left. Those
fallbacks are noted during the search, so trying them needs no further searching. If a book runs
out of listings that way, the sources its dead listing did not come from are
searched once more before the tool gives up on it.

### Archives

Some downloads wrap the whole book in a `.zip`, `.rar` or `.7z`. Once such an
item is here, each archive is unpacked into the folder it is in and then
**deleted**, so the check (and your player) sees the audio and not a box.

- Zip is handled by the tool itself. Rar and 7z are handed to the first of
  `7zz`, `7z`, `unar` or `bsdtar` that is installed; multi-part rars are
  unpacked from their first piece and all the pieces deleted.
- An archive is deleted only when everything in it came out. One that cannot
  be unpacked (no program for it, a password, damage, or it would overwrite a
  different file already there) is left exactly as it was and the tool says
  why. A book still in its archive is reported as *unchecked*, never rejected.
- Nothing in an archive can be written outside the folder the archive is in,
  and one that unpacks to far more than its own size is refused.

`-unpack=false` leaves archives alone.

### Checking what came down

A listing's description is only a claim, so once
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

### The same book in several formats

Some downloads hold the whole book two or three times: once as an M4B, again
as a folder of MP3s. The check notices when all the files of one format run,
in total, as long as all the files of another (to within 2%), keeps the best
one, and moves the others to `.audiobook-fetch/duplicates/` inside the
download folder. They are **moved, not deleted**: look there if you would
have chosen differently, and empty the folder when you are happy.

The best-sounding format is kept, going by bitrate weighted for how efficient
the codec is (AAC and Opus need less than MP3 for the same sound). Among
formats that sound as good as each other, a single M4B wins because it
carries chapters, then the one in fewer files, then the smaller one. Formats
of different lengths are never touched: a sample, a bonus track, or two
halves of a book in different formats all stay.

`-keep-one-format=false` keeps everything. It needs the check, since the
check is what measures the files.

### Without the audio tools

Both tools are optional. Without `ffprobe` nothing is checked; without
`ffmpeg`, `whisper-cli` or a model file the listening checks are skipped, and
the tool says so. The model is `-whisper-model`, else `whisper_model` in the
config, else `WHISPER_MODEL`, else the first `ggml-*.bin` in
`~/.config/audiobook-fetch/models/`.

### Sources

Each book is searched in the order the sources are given, and a later source
is only asked when the earlier ones gave no near-certain match; then the best
listing across all of them wins.

| Name | What it is |
|------|------------|
| `audiobookbay` | AudioBook Bay: audiobooks only, with author, edition, format and bitrate on each listing |
| `knaben` | Knaben: one API over The Pirate Bay and other public trackers |
| `piratebay` | The Pirate Bay directly; Knaben already covers it, so this is for when Knaben is down |

The general trackers list a release name and little else, so edition, format,
bitrate and language are read from the name where it says. They do report how
many people are seeding, and a listing nobody is seeding is never picked.

**A source refusing you?** Sources limit how fast one address may search.
The tool stops after three failures in a row instead of grinding on; wait a
while and run again, or raise `-delay`. `-search-proxy` sends searches (never
downloads) through a proxy if your network needs one.

## The parts on their own

```sh
audiobook-fetch find books.txt                  # only find links; writes results/
audiobook-fetch download magnets.txt /folder    # only download links you already have
audiobook-fetch check /folder                   # only check books already downloaded
audiobook-fetch tidy /folder                    # unpack archives and drop duplicate formats there
```

`find` writes `magnets.txt` (one link per confident match), `results.csv` and
`results.json`. `download` takes any file of magnet links. `check` treats each
subfolder as one book and prints a verdict for each (`-json` for machines); it
moves and deletes nothing, and exits non-zero if it would reject any.

`tidy` does for books already in a folder what a download gets on arrival:
[archives](#archives) are unpacked and deleted, and a book held in
[several formats](#the-same-book-in-several-formats) is cut down to the best,
with the others moved to the folder's `.audiobook-fetch/duplicates/`. Try it
with `-dry-run` first to see what it would do, and use `-book NAME` to tidy
only the folders whose name contains NAME.

## Extending

Sources and downloaders are plugins: each is one Go package that implements
a small interface and registers itself under a name. Nothing else in the
code knows which ones exist. The work is two stages joined by a magnet link:

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

Plugins are compiled in, not loaded at run time, so a new one means building
the tool yourself or sending a pull request.
[CONTRIBUTING.md](CONTRIBUTING.md#writing-a-plugin) walks through writing and
testing one.

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

From any other language, run `find` or `download` with `-json -quiet`, or
`check` with `-json`, and read stdout.

## Contributing

Bug reports and pull requests are welcome. [CONTRIBUTING.md](CONTRIBUTING.md)
covers how the code is laid out, how to run the tests, and what a change
needs before it can be merged.

## Security

What the tool trusts and what it does not is written down in
[SECURITY.md](SECURITY.md), along with how to report a problem.

## License

[MIT](LICENSE). The software is provided as is, without warranty of any kind.
