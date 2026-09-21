package share

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0x78f1935/aNLpt/internal/api"
	"github.com/0x78f1935/aNLpt/internal/auth"
	"github.com/0x78f1935/aNLpt/internal/config"
	"github.com/0x78f1935/aNLpt/internal/scan"
)

type keptTokens struct{}

func (keptTokens) Load() (auth.Tokens, error) {
	return auth.Tokens{Access: "access", Refresh: "refresh", Expires: time.Now().Add(time.Hour)}, nil
}
func (keptTokens) Save(auth.Tokens) error { return nil }
func (keptTokens) Clear() error           { return nil }

// fakeArchive takes pieces the way the real one does: in order, only so far ahead, and
// only when the checksum is right.
type fakeArchive struct {
	*httptest.Server
	mu        sync.Mutex
	window    int
	received  map[int][]byte
	delivered int
	gaveUp    string
	tooEarly  int
}

func newFakeArchive(t *testing.T) *fakeArchive {
	f := &fakeArchive{window: 2, received: map[int][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/v1/tracker/relay/client/transfer/pieces/{index}/", func(w http.ResponseWriter, r *http.Request) {
		index, _ := strconv.Atoi(r.PathValue("index"))
		body, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.UserAgent() != "aNLpt/test" || r.Header.Get("Authorization") != "Bearer access" {
			t.Errorf("piece %d came from %q with %q", index, r.UserAgent(), r.Header.Get("Authorization"))
		}
		if index >= f.delivered+f.window {
			f.tooEarly++
			w.WriteHeader(http.StatusConflict)
			return
		}
		if hex.EncodeToString(sum[:]) != r.Header.Get("X-Piece-Sha256") {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		f.received[index] = body
		// The downloader takes a piece for every one that arrives, one behind.
		if index > 0 {
			f.delivered = index
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"send_until": f.delivered + f.window})
	})
	mux.HandleFunc("GET /api/v1/tracker/client/client/poll/", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.delivered++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ask_again_in": 1,
			"wanted":       []map[string]any{{"id": "transfer", "send_until": f.delivered + f.window}},
		})
	})
	mux.HandleFunc("POST /api/v1/tracker/relay/client/transfer/give-up/", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Reason string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.gaveUp = body.Reason
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeArchive) file(pieces int) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	var whole []byte
	for index := 0; index < pieces; index++ {
		whole = append(whole, f.received[index]...)
	}
	return whole
}

func engineFor(t *testing.T, f *fakeArchive) *Engine {
	t.Setenv("ANLPT_HOME", t.TempDir())
	store, err := config.Open()
	if err != nil {
		t.Fatal(err)
	}
	session := &auth.Session{Server: f.URL, ClientID: "anlpt", UserAgent: "aNLpt/test", Vault: keptTokens{}, HTTP: f.Client()}
	client := &api.Client{Server: f.URL, UserAgent: "aNLpt/test", Session: session, HTTP: f.Client(), ID: "client"}
	return New(store, session, client, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func shareOne(t *testing.T, e *Engine, content []byte) (scan.File, string) {
	root := t.TempDir()
	path := filepath.Join(root, "Alfred J. Kwak", "S01E01.mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	found, err := scan.Folder("folder", root)
	if err != nil || len(found) != 1 {
		t.Fatalf("found %v, %v", found, err)
	}
	e.index.Replace("folder", found)
	shared := config.Folder{ID: "folder", Path: root, Visibility: "members", Language: "nl", Downloadable: true}
	if err := e.Config.Update(func(cfg *config.Config) { cfg.Folders = []config.Folder{shared} }); err != nil {
		t.Fatal(err)
	}
	return found[0], path
}

func wantedFor(file scan.File, pieceSize int64) api.Wanted {
	pieces := int((file.Size + pieceSize - 1) / pieceSize)
	return api.Wanted{ID: "transfer", File: file.ID, Size: file.Size, MTime: file.ModTime, PieceSize: pieceSize, Pieces: pieces, SendUntil: 2}
}

func TestAFileArrivesAsItLeft(t *testing.T) {
	f := newFakeArchive(t)
	e := engineFor(t, f)
	content := bytes.Repeat([]byte("Dutch television, one piece at a time. "), 300) // about 11 KB
	file, _ := shareOne(t, e, content)
	wanted := wantedFor(file, 1024)

	if err := e.send(context.Background(), wanted, &Sending{}); err != nil {
		t.Fatal(err)
	}
	if got := f.file(wanted.Pieces); !bytes.Equal(got, content) {
		t.Fatalf("%d bytes arrived, %d left", len(got), len(content))
	}
	// It was told how far it may go, and it never went further: nothing was turned away.
	if f.tooEarly != 0 {
		t.Fatalf("%d pieces were sent before they were wanted", f.tooEarly)
	}
}

func TestOnlyWhatWasSharedCanBeAskedFor(t *testing.T) {
	f := newFakeArchive(t)
	e := engineFor(t, f)
	_, path := shareOne(t, e, []byte("video"))

	for _, asked := range []string{path, "../../etc/passwd", `C:\Windows\win.ini`, ""} {
		err := e.send(context.Background(), api.Wanted{ID: "transfer", File: asked, Size: 5, PieceSize: 1024, Pieces: 1, SendUntil: 1}, &Sending{})
		if err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("asking for %q: %v", asked, err)
		}
	}
	if len(f.received) != 0 {
		t.Fatal("something was sent that was never shared")
	}
	if f.gaveUp != "missing" {
		t.Fatalf("the archive was told %q", f.gaveUp)
	}
}

func TestAFileThatChangedIsNotSent(t *testing.T) {
	f := newFakeArchive(t)
	e := engineFor(t, f)
	file, path := shareOne(t, e, []byte("what the archive was told about"))
	wanted := wantedFor(file, 1024)
	if err := os.WriteFile(path, []byte("something else entirely, put there since the last scan"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := e.send(context.Background(), wanted, &Sending{}); err == nil {
		t.Fatal("a file that was replaced was sent as the one that was asked for")
	}
	if f.gaveUp != "changed" || len(f.received) != 0 {
		t.Fatalf("told the archive %q after sending %d pieces", f.gaveUp, len(f.received))
	}
}

// What became of a transfer is remembered for the page: sent, stopped by the other
// side, or failed and why. Only so many, so it never grows.
func TestWhatBecameOfATransferIsRemembered(t *testing.T) {
	f := newFakeArchive(t)
	e := engineFor(t, f)

	e.mu.Lock()
	e.remember(&Sending{Name: "sent.mkv", Started: time.Now()}, nil)
	e.remember(&Sending{Name: "cancelled.mkv", Started: time.Now()}, api.ErrOver)
	e.remember(&Sending{Name: "replaced.mkv", Started: time.Now()}, errGaveUp{reason: "changed"})
	e.remember(&Sending{Name: "offline.mkv", Started: time.Now()}, io.ErrUnexpectedEOF)
	e.mu.Unlock()

	got := map[string]string{}
	for _, past := range e.Status().History {
		got[past.Name] = past.Outcome + ":" + past.Reason
	}
	want := map[string]string{
		"sent.mkv": "done:", "cancelled.mkv": "stopped:",
		"replaced.mkv": "failed:changed", "offline.mkv": "failed:connection",
	}
	for name, outcome := range want {
		if got[name] != outcome {
			t.Errorf("%s: %q, want %q", name, got[name], outcome)
		}
	}
	if newest := e.Status().History[0].Name; newest != "offline.mkv" {
		t.Errorf("the newest is %q, and it should come first", newest)
	}

	e.mu.Lock()
	for i := 0; i < 3*historyLength; i++ {
		e.remember(&Sending{Name: "more.mkv", Started: time.Now()}, nil)
	}
	e.mu.Unlock()
	if kept := len(e.Status().History); kept != historyLength {
		t.Fatalf("%d were kept, and %d is the most", kept, historyLength)
	}
}

// The archive says how fast a file may travel and the owner of the computer may say so
// too. Whichever is slower wins, and zero on either side means that side has no opinion.
func TestTheSlowerLimitWins(t *testing.T) {
	const archive = 10 * 1024 * 1024
	for _, c := range []struct{ archive, owner, want int }{
		{archive, 0, archive},
		{archive, 500 * 1024, 500 * 1024},
		{archive, 50 * 1024 * 1024, archive},
		{0, 500 * 1024, 500 * 1024},
		{0, 0, 0},
	} {
		if got := slowest(c.archive, c.owner); got != c.want {
			t.Errorf("slowest(%d, %d) = %d, want %d", c.archive, c.owner, got, c.want)
		}
	}
}

// Switched off, the program asks the archive rarely, and its owner can make it ask now:
// but pressing the button over and over does not turn into a request each time.
func TestASwitchedOffProgramAsksWhenToldToAndNoSooner(t *testing.T) {
	every, atLeast := switchedOffEvery, switchedOffAtLeast
	switchedOffEvery, switchedOffAtLeast = time.Hour, 150*time.Millisecond
	t.Cleanup(func() { switchedOffEvery, switchedOffAtLeast = every, atLeast })
	e := engineFor(t, newFakeArchive(t))

	started := time.Now()
	done := make(chan bool, 1)
	go func() { done <- e.waitSwitchedOff(context.Background()) }()
	time.Sleep(20 * time.Millisecond)
	for range 50 {
		e.TryAgain()
	}

	select {
	case again := <-done:
		if !again {
			t.Fatal("it gave up instead of asking again")
		}
		if waited := time.Since(started); waited < switchedOffAtLeast {
			t.Fatalf("asked again after %s, sooner than it may", waited)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("being told to try again did not make it ask")
	}

	// Fifty presses were one request: nothing is left over to set off the next wait.
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if e.waitSwitchedOff(ctx) {
		t.Fatal("old presses of the button made it ask a second time")
	}
}

// "Nobody" means nobody from the moment it is chosen, for a folder, a series or one file,
// whether or not the archive has heard yet.
func TestWhatIsKeptBackIsNotSent(t *testing.T) {
	archive := newFakeArchive(t)
	e := engineFor(t, archive)
	file, _ := shareOne(t, e, []byte("an episode"))
	if !e.offered(file) {
		t.Fatal("a file in a shared folder is not on offer")
	}

	keep := func(change func(*config.Folder)) {
		t.Helper()
		if err := e.Config.Update(func(cfg *config.Config) { change(&cfg.Folders[0]) }); err != nil {
			t.Fatal(err)
		}
	}
	keep(func(folder *config.Folder) { folder.SetAudience([]string{"Alfred J. Kwak"}, "private") })
	if e.offered(file) {
		t.Fatal("a series that is for nobody was still on offer")
	}
	err := e.send(context.Background(), wantedFor(file, 4), &Sending{})
	if gave, ok := err.(errGaveUp); !ok || gave.reason != "missing" {
		t.Fatalf("asked for anyway, it answered %v", err)
	}

	// And the other way round: one episode given away from a folder that is for nobody.
	keep(func(folder *config.Folder) {
		folder.Visibility, folder.Downloadable = "private", false
		folder.SetAudience([]string{"Alfred J. Kwak/S01E01.mkv"}, "public")
	})
	if !e.offered(file) {
		t.Fatal("an episode that is for everybody was not on offer")
	}
}
