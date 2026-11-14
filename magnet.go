package audiobookfetch

import (
	"encoding/base32"
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"
)

// Magnet links: the hand-off between finding and downloading.

// Magnet is one link, with its info hash pulled out.
type Magnet struct {
	Hash string // 40 lowercase hex characters
	URI  string
}

// FallbackTrackers are used when a listing names no trackers of its own.
var FallbackTrackers = []string{
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://open.stealth.si:80/announce",
	"udp://exodus.desync.com:6969/announce",
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://tracker.tiny-vps.com:6969/announce",
	"udp://tracker.dler.org:6969/announce",
	"http://tracker2.dler.org:80/announce",
	"http://tracker.bt4g.com:2095/announce",
}

var btih = regexp.MustCompile(`(?i)\bxt=urn:btih:([a-z0-9]+)`)

func escape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// BuildMagnet assembles a magnet link. With no trackers, FallbackTrackers are used.
func BuildMagnet(infoHash, name string, trackers []string) string {
	if len(trackers) == 0 {
		trackers = FallbackTrackers
	}
	var b strings.Builder
	b.WriteString("magnet:?xt=urn:btih:" + infoHash + "&dn=" + escape(name))
	for _, t := range trackers {
		b.WriteString("&tr=" + escape(t))
	}
	return b.String()
}

// ParseMagnets reads magnet links, one per line, skipping blanks, comments
// and repeats of a hash already seen. bad counts lines that held no usable link.
func ParseMagnets(text string) (magnets []Magnet, bad int) {
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\xef\xbb\xbf"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		hash := ""
		if m := btih.FindStringSubmatch(line); m != nil && strings.HasPrefix(strings.ToLower(line), "magnet:") {
			hash = NormalizeHash(m[1])
		}
		if hash == "" {
			bad++
			continue
		}
		if !seen[hash] {
			seen[hash] = true
			magnets = append(magnets, Magnet{Hash: hash, URI: line})
		}
	}
	return magnets, bad
}

// NormalizeHash accepts an info hash in hex or base32 and returns lowercase
// hex, or "" if it is neither.
func NormalizeHash(raw string) string {
	switch len(raw) {
	case 40:
		if _, err := hex.DecodeString(raw); err == nil {
			return strings.ToLower(raw)
		}
	case 32:
		if decoded, err := base32.StdEncoding.DecodeString(strings.ToUpper(raw)); err == nil {
			return hex.EncodeToString(decoded)
		}
	}
	return ""
}
