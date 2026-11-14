package audiobookbay

import (
	"strings"
	"testing"

	fetch "github.com/broots144/audiobook-fetch"
)

const searchHTML = `<div id="content">
<div class="post"><div class="postTitle"><h2><a href="/abss/first-book/" rel="bookmark">First Book - Ann Author</a></h2></div>
<div class="postInfo">Category: Adults&nbsp; Classic&nbsp; <br />Language: English<span>Keywords: One&nbsp Two&nbsp </span><br /></div>
<div class="postContent"><p>Shared by:<a href="/member">someone</a></p>
<p>Posted: 16 Dec 2025<br />Format: <span>M4B</span> / Bitrate: <span>64 Kbps</span><br />File Size: <span>419.23</span> MBs</p></div></div>
<div class="post"><div class="postTitle"><h2><a href="https://other.example/abss/second/">Second Book - B. Writer</a></h2></div>
<div class="postInfo">Category: Kids <br />Language: German<br /></div>
<div class="postContent"><p>Posted: 1 Jan 2024<br />Format: <span>MP3</span> / Bitrate: <span>Mixed</span><br />File Size: <span>1.67</span> GBs</p></div></div>
<div class="post"><div class="postTitle"><h2>no link here</h2></div></div>
</div>`

func TestParseSearch(t *testing.T) {
	got, err := ParseSearch(searchHTML, "https://site.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2", len(got))
	}
	first := fetch.Candidate{Heading: "First Book - Ann Author", URL: "https://site.example/abss/first-book/",
		Language: "English", Format: "M4B", Bitrate: "64 Kbps", Size: "419.23 MBs", Posted: "16 Dec 2025"}
	if g := *got[0]; g.Heading != first.Heading || g.URL != first.URL || g.Language != first.Language ||
		g.Format != first.Format || g.Bitrate != first.Bitrate || g.Size != first.Size || g.Posted != first.Posted {
		t.Errorf("first = %+v\nwant   %+v", g, first)
	}
	if g := got[1]; g.URL != "https://other.example/abss/second/" || g.Language != "German" || g.Bitrate != "Mixed" || g.Size != "1.67 GBs" {
		t.Errorf("second = %+v", *g)
	}
}

const detailsHTML = `<div class="post"><div class="postTitle"><h1 itemprop="name">First Book: Full Heading - Ann Author</h1></div>
<div class="desc"><p>Written by <a href="/?s=ann"><span class="author">Ann Author</span></a> <br />
Read by <a href="/?s=r1"><span class="narrator">Reader One</span></a>, <a href="/?s=r2"><span class="narrator">Reader Two</span></a><br />
Format: <span class="format">M4B</span> <br /><span class="is_abridged">Unabridged</span></p></div>
<table>
<tr><td>Tracker:</td><td>udp://one.example:1337/announce</td></tr>
<tr><td>Tracker:</td><td>http://two.example:80/announce</td></tr>
<tr><td>Tracker:</td><td>udp://one.example:1337/announce</td></tr>
<tr><td colspan='2'>This is a Multifile Torrent</td></tr>
<tr><td>Info Hash:</td><td>6D9A901038D432F3EC1DF3E7906CEDEECCEFD5E8</td></tr>
</table>
<ul><li><span class="commentAuthor">not an author</span></li></ul></div>`

func TestParseDetails(t *testing.T) {
	c := &fetch.Candidate{Heading: "First Book - Ann Author"}
	if err := ParseDetails(detailsHTML, c); err != nil {
		t.Fatal(err)
	}
	if c.Heading != "First Book: Full Heading - Ann Author" {
		t.Errorf("heading = %q", c.Heading)
	}
	if strings.Join(c.Authors, "|") != "Ann Author" || strings.Join(c.Narrators, "|") != "Reader One|Reader Two" {
		t.Errorf("authors = %v, narrators = %v", c.Authors, c.Narrators)
	}
	if c.Edition != "Unabridged" {
		t.Errorf("edition = %q", c.Edition)
	}
	if c.InfoHash != "6d9a901038d432f3ec1df3e7906cedeeccefd5e8" {
		t.Errorf("hash = %q", c.InfoHash)
	}
	if strings.Join(c.Trackers, "|") != "udp://one.example:1337/announce|http://two.example:80/announce" {
		t.Errorf("trackers = %v", c.Trackers)
	}
}

func TestRegistered(t *testing.T) {
	src, err := fetch.NewSource("audiobookbay", fetch.SourceConfig{})
	if err != nil || src.Name() != "AudioBook Bay" {
		t.Fatalf("source %v, err %v", src, err)
	}
	t.Setenv("AUDIOBOOKBAY_URL", "https://elsewhere.example/")
	site, _ := New(fetch.SourceConfig{})
	if site.BaseURL != "https://elsewhere.example" {
		t.Errorf("BaseURL = %q", site.BaseURL)
	}
}
