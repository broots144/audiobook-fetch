# Extending

How to add a place to search or a service to download through, and how to use the package from your own Go code.

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

`downloaders/torbox` is the model for a service with an account, and
`downloaders/archive` for one that serves files directly. An API key is read from
`<NAME>_API_KEY` or `~/.config/audiobook-fetch/<name>.key`.

Either one registers itself from `init` with `fetch.RegisterSource` or
`fetch.RegisterDownloader`, and is switched on by adding a blank import to
`cmd/audiobook-fetch/main.go`. Its name then works with `-source` or
`-downloader` and in `config.json`. With several sources, each book is
searched in the order given, and later sources are skipped once an earlier
one yields a near-certain match.

Plugins are compiled in, not loaded at run time, so a new one means building
the tool yourself or sending a pull request.
[CONTRIBUTING.md](../CONTRIBUTING.md#writing-a-plugin) walks through writing and
testing one.

## Use from Go

The package is usable as a library, but it is pre-1.0: names and signatures
may still change between minor versions.

```go
import (
    fetch "github.com/broots144/audiobook-fetch"
    _ "github.com/broots144/audiobook-fetch/downloaders/torbox"
    _ "github.com/broots144/audiobook-fetch/sources/archive"
)

source, _ := fetch.NewSource("archive", fetch.SourceConfig{Delay: 3 * time.Second})
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
