package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		candidate, current string
		newer              bool
	}{
		{"v1.1.0", "1.0.0", true},
		{"1.0.1", "1.0.0", true},
		{"v2.0.0", "1.9.9", true},
		{"v1.10.0", "1.9.0", true},
		{"v1.0.0", "1.0.0", false},
		{"v0.9.0", "1.0.0", false},
		{"v1.1.0-rc1", "1.0.0", false},
		{"v1.1.0", "dev", false},
		{"nonsense", "1.0.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.candidate, c.current); got != c.newer {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.candidate, c.current, got, c.newer)
		}
	}
}

// github answers as GitHub does, with the release given, and counts the questions.
func github(t *testing.T, body string) (*httptest.Server, *int) {
	t.Helper()
	asked := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		if r.Header.Get("User-Agent") != "aNLpt/1.0.0" {
			t.Errorf("asked as %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &asked
}

func checker(url string) *Checker {
	return &Checker{Current: "1.0.0", UserAgent: "aNLpt/1.0.0", HTTP: http.DefaultClient, URL: url}
}

func TestANewerReleaseIsFound(t *testing.T) {
	server, _ := github(t, `{"tag_name":"v1.1.0","html_url":"https://github.com/0x78f1935/aNLpt/releases/tag/v1.1.0"}`)
	found := checker(server.URL)
	if err := found.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	newer := found.Newer()
	if newer == nil || newer.Version != "1.1.0" || newer.URL != "https://github.com/0x78f1935/aNLpt/releases/tag/v1.1.0" {
		t.Fatalf("found %+v", newer)
	}
}

func TestNothingIsSaidOfTheSameVersionAPreReleaseOrAPageElsewhere(t *testing.T) {
	for _, body := range []string{
		`{"tag_name":"v1.0.0","html_url":"https://github.com/0x78f1935/aNLpt/releases/tag/v1.0.0"}`,
		`{"tag_name":"v1.1.0","html_url":"https://github.com/0x78f1935/aNLpt/releases/tag/v1.1.0","prerelease":true}`,
		`{"tag_name":"v1.1.0","html_url":"https://example.com/aNLpt.exe"}`,
	} {
		server, _ := github(t, body)
		found := checker(server.URL)
		if err := found.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
		if found.Newer() != nil {
			t.Errorf("%s: said %+v", body, found.Newer())
		}
	}
}

func TestAskedAfterAWhileAndThenNowAndThen(t *testing.T) {
	server, asked := github(t, `{"tag_name":"v1.1.0","html_url":"https://github.com/0x78f1935/aNLpt/releases/tag/v1.1.0"}`)
	found := checker(server.URL)
	found.First, found.Every = 20*time.Millisecond, time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	found.Run(ctx)
	if *asked != 1 {
		t.Fatalf("asked %d times, want once in the first day", *asked)
	}
	if found.Newer() == nil {
		t.Fatal("found nothing")
	}
}

func TestABuildOfOnesOwnNeverAsks(t *testing.T) {
	server, asked := github(t, `{"tag_name":"v1.1.0","html_url":"https://github.com/0x78f1935/aNLpt/releases/tag/v1.1.0"}`)
	found := checker(server.URL)
	found.Current = "dev"
	found.First = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	found.Run(ctx)
	if *asked != 0 {
		t.Fatalf("a dev build asked %d times", *asked)
	}
}
