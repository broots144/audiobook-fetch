# Behaviour in detail

What the tool does with a download once it has it, and why. The [README](../README.md) has the short version.

## Removing from the downloader

**`-remove-after`** only ever removes items this tool added, and only once
every file of the item is on disk. Anything that was already in your account
is left alone, even if it matches a link in the list.

## Queues

Some accounts may only fetch a few links at once and queue the
rest. The tool starts queued links itself as slots free up.

## Links with no seeds

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

## Archives

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

## Checking what came down

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

## The same book in several formats

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

## Without the audio tools

Both tools are optional. Without `ffprobe` nothing is checked; without
`ffmpeg`, `whisper-cli` or a model file the listening checks are skipped, and
the tool says so. The model is `-whisper-model`, else `whisper_model` in the
config, else `WHISPER_MODEL`, else the first `ggml-*.bin` in
`~/.config/audiobook-fetch/models/`.

## Sources

Each book is searched in the order the sources are given, and a later source
is only asked when the earlier ones gave no near-certain match; then the best
listing across all of them wins.

| Name | What it is |
|------|------------|
| `archive` | The Internet Archive's LibriVox collection: public-domain books read by volunteers, free to download |
| `audiobookbay` | AudioBook Bay: audiobooks only, with author, edition, format and bitrate on each listing |
| `knaben` | Knaben: one API over The Pirate Bay and other public trackers |
| `piratebay` | The Pirate Bay directly; Knaben already covers it, so this is for when Knaben is down |

**Downloaders.** A link is sent to the downloader that can fetch it:

| Name | What it is |
|------|------------|
| `archive` | fetches Internet Archive items straight from archive.org over HTTPS; no account, and only the original audio and the cover are taken, not the re-encoded copies the Archive keeps beside them |
| `torbox` | [TorBox](https://torbox.app), for every other link; needs an account and its API key |

The Archive's links always go to `archive` whichever downloader is chosen,
because a torrent service makes no headway with them. So a list can mix
both kinds of book, and with no TorBox key the public-domain ones still
arrive while the rest are reported as needing a key.

The general trackers list a release name and little else, so edition, format,
bitrate and language are read from the name where it says. They do report how
many people are seeding, and a listing nobody is seeding is never picked.

**A source refusing you?** Sources limit how fast one address may search.
The tool stops after three failures in a row instead of grinding on; wait a
while and run again, or raise `-delay`. `-search-proxy` sends searches (never
downloads) through a proxy if your network needs one.
