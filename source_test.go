package audiobookfetch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetcherSaysWhoItIs(t *testing.T) {
	var agent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agent = r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, "page")
	}))
	defer server.Close()

	f, err := NewFetcher(SourceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if body, err := f.Get(server.URL); err != nil || body != "page" {
		t.Fatalf("body %q, err %v", body, err)
	}
	// By its own name and where to read about it, not dressed up as a browser.
	if agent != UserAgent || !strings.HasPrefix(agent, AppName) || !strings.Contains(agent, "github.com/broots144/audiobook-fetch") ||
		strings.Contains(agent, "Mozilla") {
		t.Errorf("user agent: %q", agent)
	}
}

func TestFetcherStopsWhenToldTo(t *testing.T) {
	asked := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		http.Error(w, "busy", http.StatusServiceUnavailable) // worth retrying, after a long wait
	}))
	defer server.Close()

	// Stopped before it starts: nothing is asked for at all.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f, _ := NewFetcher(SourceConfig{Context: ctx})
	if _, err := f.Get(server.URL); !errors.Is(err, context.Canceled) || asked != 0 {
		t.Errorf("already stopped: err %v, %d requests", err, asked)
	}

	// Stopped while waiting to retry: it gives up at once instead of
	// sitting out the wait and trying again.
	ctx, cancel = context.WithCancel(context.Background())
	f, _ = NewFetcher(SourceConfig{Context: ctx})
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	if _, err := f.Get(server.URL); err == nil {
		t.Error("a request that never succeeded should be an error")
	}
	if took := time.Since(start); took > 3*time.Second || asked != 1 {
		t.Errorf("should stop during the first retry wait: took %s, %d requests", took, asked)
	}

	// Stopped during the pause between requests.
	ctx, cancel = context.WithCancel(context.Background())
	f, _ = NewFetcher(SourceConfig{Context: ctx, Delay: time.Hour})
	f.last = time.Now()
	time.AfterFunc(50*time.Millisecond, cancel)
	start = time.Now()
	if _, err := f.Get(server.URL); !errors.Is(err, context.Canceled) || time.Since(start) > 3*time.Second {
		t.Errorf("should stop during the pause: err %v after %s", err, time.Since(start))
	}
}
