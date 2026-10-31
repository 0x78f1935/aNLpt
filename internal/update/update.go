// Package update tells whether a newer aNLpt has been released.
//
// It asks GitHub for the latest release of aNLpt, a minute after the program starts and
// then once a day: never the archive, which has enough to do, and never more often, since
// a new version comes out every few weeks at most. Nothing is sent but the question; what
// to do about the answer is up to the member, who is shown where to download it.
//
// A build of one's own (version "dev") never asks: it is newer than anything released.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Latest is where GitHub says what the latest release of aNLpt is.
const Latest = "https://api.github.com/repos/0x78f1935/aNLpt/releases/latest"

// Release is a newer version, and the page to download it from.
type Release struct {
	Version string `json:"version"`
	URL     string `json:"url"`
}

// Checker asks now and then, and keeps what it last heard.
type Checker struct {
	// Current is this program's own version.
	Current   string
	UserAgent string
	HTTP      *http.Client
	// URL is Latest, or a test's.
	URL string
	// First and Every are how long after starting it first asks, and how often after.
	First, Every time.Duration

	mu    sync.RWMutex
	found *Release
}

// Newer is the newer release last heard of, or nil while there is none.
func (c *Checker) Newer() *Release {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.found == nil {
		return nil
	}
	found := *c.found
	return &found
}

// Run asks until ctx ends. A question that goes unanswered (no internet, GitHub busy) is
// simply asked again the next day.
func (c *Checker) Run(ctx context.Context) {
	if _, ok := parse(c.Current); !ok {
		return
	}
	wait := c.First
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		_ = c.Check(ctx)
		wait = c.Every
	}
}

// Check asks once.
func (c *Checker) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return err
	}
	// GitHub answers nobody who does not say who they are.
	request.Header.Set("User-Agent", c.UserAgent)
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := c.HTTP.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub answered %s", response.Status)
	}
	var latest struct {
		Tag        string `json:"tag_name"`
		Page       string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(response.Body).Decode(&latest); err != nil {
		return err
	}
	var found *Release
	if !latest.Draft && !latest.Prerelease && Newer(latest.Tag, c.Current) && strings.HasPrefix(latest.Page, "https://github.com/") {
		found = &Release{Version: strings.TrimPrefix(latest.Tag, "v"), URL: latest.Page}
	}
	c.mu.Lock()
	c.found = found
	c.mu.Unlock()
	return nil
}

// Newer says whether version `candidate` comes after `current` ("v1.2.0" after "1.1.3").
// A version that is not three numbers is never newer, and nothing is newer than one that
// is not (a build of one's own).
func Newer(candidate, current string) bool {
	have, ok := parse(current)
	if !ok {
		return false
	}
	want, ok := parse(candidate)
	if !ok {
		return false
	}
	for index := range want {
		if want[index] != have[index] {
			return want[index] > have[index]
		}
	}
	return false
}

// parse reads "1.2.3" (or "v1.2.3"; a "-something" after it is not a release).
func parse(version string) ([3]int, bool) {
	var numbers [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(version), "v"), ".")
	if len(parts) != 3 {
		return numbers, false
	}
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return numbers, false
		}
		numbers[index] = number
	}
	return numbers, true
}
