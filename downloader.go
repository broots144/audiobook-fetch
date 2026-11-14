package audiobookfetch

import (
	"fmt"
	"strings"
)

// Downloaders: services that turn a magnet link into downloadable files.

// File is one file inside an item.
type File struct {
	ID   string
	Path string // relative to the item, folders included, as the service reports it
	Size int64
}

// Item is one magnet link as the service holds it.
type Item struct {
	ID       string
	Hash     string // info hash, lowercase hex
	Name     string
	Ready    bool    // the service has every file and can serve them now
	State    string  // the service's own word for what it is doing
	Progress float64 // 0 to 1 while the service is still fetching
	Files    []File
}

// Downloader is an account on a service that fetches magnet links and then
// serves their files over plain HTTP.
type Downloader interface {
	// Name is how the service is shown to people, e.g. "TorBox".
	Name() string
	// List returns every item in the account.
	List() ([]Item, error)
	// CheckCached reports which hashes the service already holds and could
	// serve at once. Hashes missing from the result are not cached.
	CheckCached(hashes []string) (map[string]bool, error)
	// Add hands the service a magnet link to fetch.
	Add(magnet string) error
	// Link returns a URL one file can be downloaded from. Links are often
	// short-lived, so it is called again for every attempt.
	Link(item Item, file File) (string, error)
	// Remove deletes an item from the account.
	Remove(item Item) error
}

// DownloaderFactory creates a downloader from an account's API key.
type DownloaderFactory func(apiKey string) (Downloader, error)

var downloaderFactories = map[string]DownloaderFactory{}

// RegisterDownloader makes a downloader available under name. Call it from init.
func RegisterDownloader(name string, factory DownloaderFactory) { downloaderFactories[name] = factory }

// NewDownloader creates the downloader registered under name.
func NewDownloader(name, apiKey string) (Downloader, error) {
	factory, ok := downloaderFactories[name]
	if !ok {
		return nil, fmt.Errorf("unknown downloader %q (available: %s)", name, strings.Join(DownloaderNames(), ", "))
	}
	return factory(apiKey)
}

// DownloaderNames lists the registered downloaders.
func DownloaderNames() []string { return sortedKeys(downloaderFactories) }
