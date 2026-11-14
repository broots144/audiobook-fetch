# Contributing

Thanks for wanting to help. This is a small spare-time project, so the rules
are few.

## Ground rules

- **Legal use only.** The tool is for audiobooks its user is legally allowed
  to acquire. Issues and pull requests asking for help getting specific
  copyrighted works, or for ways around a site's access controls or rate
  limits, will be closed.
- **No secrets, no personal data.** Never put an API key, a real download
  list, or output from your own library in an issue, a commit or a test.
- **Security problems go in private.** See [SECURITY.md](SECURITY.md).

## Getting set up

You need Go 1.27 or newer. Nothing else: the tests fake the sources, the
download service, ffmpeg and whisper, so they need no network and no account.

```sh
git clone https://github.com/broots144/audiobook-fetch
cd audiobook-fetch
go build -o bin/audiobook-fetch ./cmd/audiobook-fetch
go test ./...
```

## Before you open a pull request

CI runs these on every pull request, and all of them must pass:

```sh
gofmt -l .                 # prints nothing when the formatting is right
go vet ./...
go test -race ./...
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

And please:

- **Add a test** for what you changed. Tests must not touch the network or
  need outside programs; follow the fakes in `download_test.go` and
  `verify_test.go`.
- **Keep it small.** One change per pull request, with a message that says
  what changed and why.
- **Update the README** when behaviour or an option changes.
- **Avoid new dependencies** unless there is no reasonable way around one.
- **Treat everything from outside as untrusted**: listing pages, file names
  inside a download, and the downloaded files themselves. See SECURITY.md
  for what the code already guarantees, and do not weaken it.

## How the code is laid out

The work is two stages joined by a magnet link:

```
list ──▶ Source ──▶ best listing ──▶ magnet link ──▶ Downloader ──▶ files ──▶ check
```

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
| `verify.go` | checking the audio of what was downloaded |
| `sources/` | one package per place to search |
| `downloaders/` | one package per download service |
| `cmd/audiobook-fetch/` | the command line |

## Writing a plugin

Where the tool searches and what fetches the links are plugins, and adding
one is the most self-contained way to contribute. The shared code does the
hard parts (reading the list, scoring, choosing, resumable downloads, the
audio check); a plugin only has to speak to its own service.

| Kind | Interface | Lives in | Copy from |
|------|-----------|----------|-----------|
| Source: somewhere to search | `Source` in `source.go` (3 methods) | `sources/<name>/` | `sources/audiobookbay` for a site read page by page, `sources/knaben` for a JSON API |
| Downloader: turns a magnet link into files | `Downloader` in `downloader.go` (6 methods) | `downloaders/<name>/` | `downloaders/torbox` |

The steps are the same for both:

1. **Implement the interface** in a new package. The README's
   [Extending](README.md#extending) section lists the methods. A downloader
   whose service queues links can also implement the optional `Queuer`.
2. **Register it from `init`** under the name people will type:

   ```go
   func init() {
       fetch.RegisterSource("mysite", func(cfg fetch.SourceConfig) (fetch.Source, error) {
           return New(cfg)
       })
   }
   ```

   Downloaders use `fetch.RegisterDownloader`, and are handed the API key
   read from `<NAME>_API_KEY` or `~/.config/audiobook-fetch/<name>.key`.
3. **Switch it on** with a blank import in `cmd/audiobook-fetch/main.go`. The
   name then works with `-source` or `-downloader` and in `config.json`.
4. **Test it against saved answers**, never the live service: see the
   `_test.go` file next to each existing plugin.
5. **Document it** in the README's sources table or downloader notes.

For a source, use `fetch.NewFetcher` for requests: it paces them, retries,
honours the proxy option and caps answer sizes. `fetch.ParseRelease` unpacks
a tracker-style release name. Keep the default pace polite; a source exists
at the pleasure of the site it reads.

A new plugin must be for a service that has lawful uses, and must not get
around a login, a paywall or other access control.

## License

By contributing you agree that your contribution is released under the
project's [MIT license](LICENSE).
