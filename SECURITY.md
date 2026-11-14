# Security

## Reporting a problem

Please report security problems privately, through
[GitHub's private vulnerability reporting](https://github.com/broots144/audiobook-fetch/security/advisories/new),
rather than in a public issue. This is a spare-time project with no promised
response time, but reports are read and fixes are released as soon as they
are ready. Only the latest commit on `main` is supported.

## What the tool handles

**Your API key.** The downloader's key is read from an environment variable
or a key file, never from the command line, and is not written to the config
file, the state file, reports or logs. It is sent only to the downloader's
own API over HTTPS. One TorBox request needs the key in its URL; URLs and
their query strings are stripped from error messages before they are shown
or saved. Keep the key file private (`chmod 600`); the tool warns if it is
not.

**Untrusted input.** Search results, listing pages, file names inside a
download and the downloaded files themselves all come from strangers:

- File names are cleaned and every file is confined to the download folder.
  A name cannot climb out of it with `..` or an absolute path, and cannot
  write into the tool's own `.audiobook-fetch/` folder.
- Downloaded files are never executed. The audio check hands them to
  `ffprobe`, `ffmpeg` and `whisper-cli` as arguments (no shell), restricted
  to reading local files, with a time limit. Those programs do parse
  untrusted media, so keep them up to date.
- Archives (zip, rar, 7z) are unpacked into the folder they are in and
  nowhere else. Names inside are cleaned like any other file name, links and
  other special entries are skipped, and existing files are never
  overwritten. Zip is read by the tool itself, with a limit on how far an
  archive may expand. Rar and 7z are unpacked by an outside program
  (`7zz`, `7z`, `unar` or `bsdtar`) into a scratch folder first, and only
  plain files are moved out of it; that program does parse untrusted data
  and has no expansion limit of ours, so keep it up to date.
- The only thing the tool ever deletes is an archive it has fully unpacked.
  A download the check rejects, and the extra copies of a book that came in
  several formats, are moved aside, not deleted.
- Answers from sources are size-limited, and text from them is written to
  the CSV report in a form spreadsheets will not run as a formula.

**What it leaves on disk.** The state file and the reports in a download
folder's `.audiobook-fetch/` say what was searched for and downloaded, so
they are written readable by you alone (mode 600, in a folder of mode 700).

**Releases.** Release binaries are built by GitHub Actions from the tagged
commit, and each release carries SHA-256 checksums and signed build
provenance: `gh attestation verify <file> --repo broots144/audiobook-fetch`.

**The network.** The tool talks to the sources you enable, to the
downloader, and to the download links the downloader hands back. Searches go
through `-search-proxy` if you set one; downloads never do. Nothing else is
contacted and nothing is reported anywhere.

## What it does not do

It does not check that a download is free of malware beyond "this is
playable audio", and it does not check what you are entitled to download.
