# Changelog

Notable changes, newest first. Versions follow [semantic versioning](https://semver.org);
before 1.0 the Go API may change between minor versions.

## Unreleased

- **Ctrl-C stops a run cleanly.** Downloads in flight stop at once and keep
  what they have; an item already being checked is finished first. The tool
  says how to carry on and exits with code 130. A second Ctrl-C stops it
  dead. Library users get the same through a `Context` on
  `DownloadOptions` and `SourceConfig`.
- **The tool says who it is.** Sources are asked under the tool's own name
  instead of a browser's. AudioBook Bay is the exception, left as it was
  because a search under the new name could not be seen to work.
- **Tested on macOS and Windows** as well as Linux, on every change. The
  config folder is `%AppData%\audiobook-fetch` on Windows and honours
  `XDG_CONFIG_HOME`; it is unchanged on macOS and Linux.
- The README is shorter; the detailed behaviour and the plugin guide moved
  to `docs/`.
- A run killed while unpacking a zip no longer leaves a half-written file
  that blocks the next attempt, and the state is saved as soon as an
  archive is unpacked.

## v0.1.0

The first release.

- **Find.** A list of books in, the best listing of each out, scored on
  title, author, language, edition and audio quality. Reads plain lists,
  CSV, TSV and JSON.
- **Sources.** The Internet Archive's LibriVox collection (public-domain
  books, searched first), AudioBook Bay, Knaben and The Pirate Bay.
- **Downloaders.** The Internet Archive directly over HTTPS, with no
  account, and TorBox for everything else. Downloads resume, queued links
  are started as slots free up, and links nobody is seeding are given up on
  in favour of the next-best listing.
- **Archives.** Zip, rar and 7z downloads are unpacked and the archive
  deleted.
- **Check.** Each download's audio is examined: playable, bitrate, spoken
  language, abridged or dramatized. What is not the book is set aside and
  the next-best listing tried.
- **One format.** A book that arrives in several formats is cut down to
  the best; the others are moved aside.
- **Commands.** `find`, `download`, `check`, `tidy`, `doctor`, `version`.
- Prebuilt binaries for macOS, Linux and Windows, with checksums and build
  provenance, and a Homebrew formula (`brew install broots144/tap/audiobook-fetch`).
