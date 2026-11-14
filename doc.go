// Package audiobookfetch turns a list of wanted audiobooks into files in a folder.
//
// The work is two stages joined by a magnet link:
//
//	list ──▶ Source ──▶ best listing ──▶ magnet link ──▶ Downloader ──▶ files
//
// A Source is somewhere to search for listings (see sources/audiobookbay).
// A Downloader is a service that turns a magnet link into files that can be
// downloaded (see downloaders/torbox). Everything else lives in this package
// and is shared by all of them: reading the list, scoring listings, choosing
// the best one, and saving files to disk in a way that survives interruption.
//
// # Adding a source
//
// Create a package under sources/ with a type that implements Source, and
// register it from init:
//
//	func init() {
//		audiobookfetch.RegisterSource("mysite", func(cfg audiobookfetch.SourceConfig) (audiobookfetch.Source, error) {
//			return New(cfg)
//		})
//	}
//
// Search returns Candidates filled in from a results page; Details fills in
// the rest, including the info hash. Fetcher gives you paced, retried page
// fetches so a new source does not have to write its own.
//
// # Adding a downloader
//
// Create a package under downloaders/ with a type that implements
// Downloader, and register it from init with RegisterDownloader. The five
// methods are: list the account, check what is cached, add a magnet, get a
// link for a file, and remove an item.
//
// Then add a blank import of the new package in cmd/audiobook-fetch/main.go.
// Its name becomes usable with -source or -downloader, and in config.json.
package audiobookfetch

// AppName names the tool's own files: its config folder and the bookkeeping
// folder it keeps inside a download folder.
const AppName = "audiobook-fetch"
