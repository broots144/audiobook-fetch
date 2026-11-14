# Changelog

Notable changes, newest first. Versions follow [semantic versioning](https://semver.org);
before 1.0 the Go API may change between minor versions.

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
