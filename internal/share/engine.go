// Package share is the part of aNLpt that does the work: it keeps the archive told about
// what is in the shared folders, asks every few seconds whether anybody wants a file, and
// sends one when somebody does.
package share

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/0x78f1935/aNLpt/internal/api"
	"github.com/0x78f1935/aNLpt/internal/auth"
	"github.com/0x78f1935/aNLpt/internal/config"
	"github.com/0x78f1935/aNLpt/internal/scan"
)

// reportBatch is how many files go in one piece of a report. The archive takes 500.
const reportBatch = 400

// State is what the program is doing, in one word, for the tray and the pages.
type State string

const (
	SignedOut State = "signed_out"
	Starting  State = "starting"
	Sharing   State = "sharing"
	Offline   State = "offline" // the archive cannot be reached; it keeps trying
	Revoked   State = "revoked" // switched off by whoever runs the archive
	Paused    State = "paused"
)

// Status is everything the pages show.
type Status struct {
	State      State           `json:"state"`
	Problem    string          `json:"problem"`
	Member     *api.Member     `json:"member"`
	Files      int             `json:"files"`
	Bytes      int64           `json:"bytes"`
	Sending    []Sending       `json:"sending"`
	History    []Sending       `json:"history"`
	Sent       int             `json:"sent"` // files sent since the program started
	LastScan   *time.Time      `json:"last_scan"`
	Scanning   bool            `json:"scanning"`
	Recognised []api.Directory `json:"directories"`
}

// Sending is one file on its way to somebody, or one that was.
type Sending struct {
	Name   string `json:"name"`
	Pieces int    `json:"pieces"`
	Done   int    `json:"done"`
	Size   int64  `json:"size"`
	// Outcome is empty while it is on its way, and then "done", "stopped" (the other
	// side ended it: a cancelled download, a closed tab) or "failed".
	Outcome string `json:"outcome"`
	// Reason says why it failed, in a word the page has a sentence for.
	Reason  string     `json:"reason"`
	Started time.Time  `json:"started"`
	Ended   *time.Time `json:"ended"`
}

// What became of recent transfers is kept in memory and nowhere else: the last few, for
// a day, gone when the program stops. It is there to answer "did that go through?", and
// the archive keeps the record that lasts. Nothing is written to disk, so a program
// forgotten in a corner for a year has not grown by a byte.
const (
	historyLength = 20
	historyAge    = 24 * time.Hour
)

// stalledAfter is how long a transfer may make no progress before this side gives up on
// it. The archive ends a transfer whose downloader went quiet, but a program should not
// depend on being told: a file shown as "being sent" for an hour is a lie either way.
const stalledAfter = 3 * time.Minute

// errGaveUp is this side deciding not to send a file after all.
type errGaveUp struct{ reason string }

func (e errGaveUp) Error() string { return "gave up on a transfer: " + e.reason }

// Engine runs until its context ends.
type Engine struct {
	Config  *config.Store
	Session *auth.Session
	API     *api.Client
	Version string
	Log     *slog.Logger

	index *scan.Index

	// told is what the archive was last told about each folder, so only changes are sent
	// and a folder taken out of the configuration is taken out over there too.
	toldMu sync.Mutex
	told   map[string]config.Folder

	mu      sync.RWMutex
	status  Status
	active  map[string]*Sending // by transfer id
	history []Sending           // newest first
	paused  bool
	running context.Context
	rescan  chan struct{}
	changed chan struct{}
	again   chan struct{}
}

// New makes an engine that is not running yet.
func New(cfg *config.Store, session *auth.Session, client *api.Client, version string, log *slog.Logger) *Engine {
	return &Engine{
		Config: cfg, Session: session, API: client, Version: version, Log: log,
		index:   scan.NewIndex(),
		told:    map[string]config.Folder{},
		active:  map[string]*Sending{},
		rescan:  make(chan struct{}, 1),
		changed: make(chan struct{}, 1),
		again:   make(chan struct{}, 1),
		status:  Status{State: SignedOut},
	}
}

// Status returns what the program is doing right now.
func (e *Engine) Status() Status {
	e.mu.RLock()
	defer e.mu.RUnlock()
	status := e.status
	status.Files, status.Bytes = e.index.Count()
	status.Sending = make([]Sending, 0, len(e.active))
	for _, sending := range e.active {
		status.Sending = append(status.Sending, *sending)
	}
	sort.Slice(status.Sending, func(i, j int) bool { return status.Sending[i].Started.Before(status.Sending[j].Started) })
	status.History = make([]Sending, 0, len(e.history))
	for _, past := range e.history {
		if past.Ended != nil && time.Since(*past.Ended) < historyAge {
			status.History = append(status.History, past)
		}
	}
	if e.paused && status.State == Sharing {
		status.State = Paused
	}
	return status
}

func (e *Engine) set(change func(*Status)) {
	e.mu.Lock()
	change(&e.status)
	e.mu.Unlock()
}

// Files lists what one shared folder was last found to hold.
func (e *Engine) Files(folderID string) []scan.File { return e.index.InFolder(folderID) }

// Rescan asks for the folders to be looked through now rather than later.
func (e *Engine) Rescan() {
	select {
	case e.rescan <- struct{}{}:
	default:
	}
}

// TryAgain asks the archive now whether a switched off program may share again. It
// does nothing in any other state.
func (e *Engine) TryAgain() {
	select {
	case e.again <- struct{}{}:
	default:
	}
}

// SettingsChanged says the folders or how they are shared changed.
func (e *Engine) SettingsChanged() {
	select {
	case e.changed <- struct{}{}:
	default:
	}
	e.Rescan()
}

// Pause stops answering requests for files without signing out. Nothing is unshared:
// the library keeps what it has, and the computer simply looks switched off.
func (e *Engine) Pause(paused bool) {
	e.mu.Lock()
	e.paused = paused
	ctx := e.running
	e.mu.Unlock()
	// Said to the archive too, and not waited for: pausing stops this side asking, and
	// silence takes half a minute to be noticed. Half a minute of download buttons that
	// lead nowhere.
	if ctx != nil && e.API.ID != "" {
		go func() {
			ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if err := e.API.Pause(ctx, paused); err != nil {
				e.Log.Warn("could not tell the archive about pausing", "error", err)
			}
		}()
	}
}

// Member is who is signed in, as the archive last described them, or nil.
func (e *Engine) Member() *api.Member {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.status.Member
}

func (e *Engine) isPaused() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.paused
}

// Run keeps going until ctx ends, through sign-outs, lost connections and everything else.
func (e *Engine) Run(ctx context.Context) {
	e.mu.Lock()
	e.running = ctx
	e.mu.Unlock()
	for ctx.Err() == nil {
		if !e.Session.SignedIn() {
			e.set(func(s *Status) { *s = Status{State: SignedOut} })
			if !sleep(ctx, 2*time.Second) {
				return
			}
			continue
		}
		e.set(func(s *Status) { s.State, s.Problem = Starting, "" })
		err := e.session(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, auth.ErrSignedOut):
			e.set(func(s *Status) { *s = Status{State: SignedOut} })
		case errors.Is(err, api.ErrRevoked):
			e.set(func(s *Status) { s.State, s.Problem = Revoked, "" })
			if !e.waitSwitchedOff(ctx) {
				return
			}
		default:
			e.Log.Warn("lost the archive", "error", err)
			e.set(func(s *Status) { s.State, s.Problem = Offline, err.Error() })
			if !sleep(ctx, 30*time.Second) {
				return
			}
		}
	}
}

// session is one stretch of being signed in and connected.
func (e *Engine) session(ctx context.Context) error {
	cfg := e.Config.Get()
	host, _ := os.Hostname()
	if err := e.API.Register(ctx, cfg.DeviceID, host, runtime.GOOS, e.Version); err != nil {
		return err
	}
	// Start from what the archive has rather than from what was said last time: somebody
	// else may have signed in, and a folder may have been taken out of the configuration
	// while the program was not running. Anything the archive has that is not shared here
	// any more is then removed by syncFolders like any other.
	shared, err := e.API.FolderIDs(ctx)
	if err != nil {
		return err
	}
	e.toldMu.Lock()
	e.told = map[string]config.Folder{}
	for _, id := range shared {
		e.told[id] = config.Folder{ID: id}
	}
	e.toldMu.Unlock()
	if err := e.refreshMember(ctx); err != nil {
		return err
	}
	if err := e.syncFolders(ctx); err != nil {
		return err
	}
	e.set(func(s *Status) { s.State, s.Problem = Sharing, "" })

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	failed := make(chan error, 2)
	go func() { failed <- e.scanLoop(ctx) }()
	go func() { failed <- e.pollLoop(ctx) }()
	return <-failed
}

func (e *Engine) refreshMember(ctx context.Context) error {
	member, err := e.API.Me(ctx)
	if err != nil {
		return err
	}
	e.set(func(s *Status) { s.Member = &member })
	// Until its owner picks a language here, the program speaks the one they read the
	// archive in. Somebody who set their profile to English should not be met in Dutch.
	cfg := e.Config.Get()
	preferred := member.PreferredLanguage
	if !cfg.LanguageChosen && (preferred == "nl" || preferred == "en") && preferred != cfg.Language {
		_ = e.Config.Update(func(c *config.Config) { c.Language = preferred })
	}
	return nil
}

// syncFolders tells the archive which folders are shared and how, and which no longer are.
func (e *Engine) syncFolders(ctx context.Context) error {
	cfg := e.Config.Get()
	e.toldMu.Lock()
	defer e.toldMu.Unlock()
	wanted := map[string]config.Folder{}
	for _, folder := range cfg.Folders {
		wanted[folder.ID] = folder
		if reflect.DeepEqual(e.told[folder.ID], folder) {
			continue
		}
		err := e.API.PutFolder(ctx, folder.ID, api.FolderSettings{
			Label:          filepath.Base(folder.Path),
			Visibility:     folder.Visibility,
			Anonymous:      folder.Anonymous,
			Language:       folder.Language,
			OtherLanguages: append([]string{}, folder.OtherLanguages...),
			Downloadable:   folder.Downloadable,
		})
		if err != nil {
			return err
		}
		e.told[folder.ID] = folder
	}
	for id := range e.told {
		if _, still := wanted[id]; still {
			continue
		}
		if err := e.API.DeleteFolder(ctx, id); err != nil {
			return err
		}
		e.index.Drop(id)
		delete(e.told, id)
	}
	return nil
}

func (e *Engine) scanLoop(ctx context.Context) error {
	for {
		if err := e.scanAll(ctx); err != nil {
			return err
		}
		interval := time.Duration(e.Config.Get().ScanMinutes) * time.Minute
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		case <-e.rescan:
		case <-e.changed:
		}
		if err := e.syncFolders(ctx); err != nil {
			return err
		}
	}
}

func (e *Engine) scanAll(ctx context.Context) error {
	e.set(func(s *Status) { s.Scanning = true })
	defer e.set(func(s *Status) { s.Scanning = false })

	for _, folder := range e.Config.Get().Folders {
		found, err := scan.Folder(folder.ID, folder.Path)
		if err != nil {
			// A disk that is unplugged is not a reason to tell the archive everything on
			// it is gone. Say nothing, and look again next time.
			e.Log.Warn("could not read a shared folder", "folder", filepath.Base(folder.Path), "error", err)
			continue
		}
		e.index.Replace(folder.ID, found)

		// The number of the report only has to go up, and the clock does.
		generation := time.Now().Unix()
		for start := 0; start < len(found) || start == 0; start += reportBatch {
			end := min(start+reportBatch, len(found))
			batch := make([]api.ReportedFile, 0, end-start)
			for _, file := range found[start:end] {
				language, others := folder.LanguagesOf(file.Path)
				batch = append(batch, api.ReportedFile{
					ID: file.ID, Path: file.Path, Size: file.Size, MTime: file.ModTime,
					Audience: folder.AudienceOf(file.Path), Language: language, OtherLanguages: others,
				})
			}
			if err := e.API.Report(ctx, folder.ID, generation, batch, end == len(found)); err != nil {
				return err
			}
			if end == len(found) {
				break
			}
		}
	}
	now := time.Now()
	e.set(func(s *Status) { s.LastScan = &now })

	// The archive reads the names in the background, so ask a little later what it made
	// of them. Not knowing yet is not an error.
	go func() {
		if !sleep(ctx, 8*time.Second) {
			return
		}
		if found, err := e.API.Directories(ctx); err == nil {
			e.set(func(s *Status) { s.Recognised = found })
		}
		_ = e.refreshMember(ctx)
	}()
	return nil
}

func (e *Engine) pollLoop(ctx context.Context) error {
	wait := 5 * time.Second
	sinceMember := time.Now()
	for {
		if !e.isPaused() {
			wanted, again, err := e.API.Poll(ctx)
			if err != nil {
				return err
			}
			if again > 0 {
				wait = again
			}
			for _, transfer := range wanted {
				e.start(ctx, transfer)
			}
		}
		if time.Since(sinceMember) > 5*time.Minute {
			sinceMember = time.Now()
			_ = e.refreshMember(ctx)
		}
		if !sleep(ctx, wait) {
			return ctx.Err()
		}
	}
}

// start sends one file, unless it is already being sent.
func (e *Engine) start(ctx context.Context, wanted api.Wanted) {
	e.mu.Lock()
	if _, already := e.active[wanted.ID]; already {
		e.mu.Unlock()
		return
	}
	sending := &Sending{Pieces: wanted.Pieces, Size: wanted.Size, Started: time.Now()}
	e.active[wanted.ID] = sending
	e.mu.Unlock()

	go func() {
		err := e.send(ctx, wanted, sending)
		e.mu.Lock()
		delete(e.active, wanted.ID)
		if err == nil {
			e.status.Sent++
		}
		if ctx.Err() == nil {
			e.remember(sending, err)
		}
		e.mu.Unlock()
		if err != nil && !errors.Is(err, api.ErrOver) && ctx.Err() == nil {
			e.Log.Warn("a file was not sent", "error", err)
		}
	}()
}

// offered says whether this file is for anybody at all, as things are set right now.
// The archive decides who may ask and is told of every change, but a change made a
// second ago may not have arrived, and "nobody" has to mean nobody from the moment it
// is chosen.
func (e *Engine) offered(file scan.File) bool {
	for _, folder := range e.Config.Get().Folders {
		if folder.ID != file.Folder {
			continue
		}
		if audience := folder.AudienceOf(file.Path); audience != "" {
			return audience != "private"
		}
		return folder.Downloadable && folder.Visibility != "private"
	}
	return false
}

// remember files a transfer that has ended under what became of it. Called with the
// lock held.
func (e *Engine) remember(sending *Sending, err error) {
	past := *sending
	now := time.Now()
	past.Ended = &now
	var gaveUp errGaveUp
	switch {
	case err == nil:
		past.Outcome = "done"
	case errors.Is(err, api.ErrOver):
		past.Outcome = "stopped"
	case errors.As(err, &gaveUp):
		past.Outcome, past.Reason = "failed", gaveUp.reason
	default:
		past.Outcome, past.Reason = "failed", "connection"
	}
	e.history = append([]Sending{past}, e.history...)
	kept := e.history[:0]
	for _, item := range e.history {
		if len(kept) < historyLength && item.Ended != nil && now.Sub(*item.Ended) < historyAge {
			kept = append(kept, item)
		}
	}
	e.history = kept
}

func (e *Engine) send(ctx context.Context, wanted api.Wanted, sending *Sending) error {
	// Only ever a file from the index, found by the id made here. The archive cannot
	// name a path, so it cannot ask for anything that was not shared.
	file, ok := e.index.Get(wanted.File)
	if !ok || !e.offered(file) {
		return e.giveUp(ctx, wanted.ID, "missing")
	}
	e.mu.Lock()
	sending.Name = filepath.Base(file.Path)
	e.mu.Unlock()

	handle, err := os.Open(file.Abs)
	if err != nil {
		return e.giveUp(ctx, wanted.ID, "unreadable")
	}
	defer handle.Close()

	// The slower of what the archive allows and what this computer's owner allows.
	limit := newLimiter(slowest(wanted.SendRate, e.Config.Get().UploadKBps*1024))
	piece := make([]byte, wanted.PieceSize)
	sendUntil := wanted.SendUntil
	for index := 0; index < wanted.Pieces; index++ {
		// The file has to be the one the archive was told about. One that was replaced
		// since the last scan would arrive as something other than what was asked for.
		info, err := handle.Stat()
		if err != nil || info.Size() != wanted.Size || info.ModTime().Unix() != wanted.MTime {
			e.Rescan()
			return e.giveUp(ctx, wanted.ID, "changed")
		}
		// Wait for room. The archive says how far it will take pieces, and sending past
		// that would spend this computer's uplink on a piece that is turned away.
		waiting := time.Now()
		for index >= sendUntil {
			if time.Since(waiting) > stalledAfter {
				return e.giveUp(ctx, wanted.ID, "stalled")
			}
			if !sleep(ctx, time.Second) {
				return ctx.Err()
			}
			again, _, err := e.API.Poll(ctx)
			if err != nil {
				return err
			}
			sendUntil = -1
			for _, other := range again {
				if other.ID == wanted.ID {
					sendUntil = other.SendUntil
				}
			}
			if sendUntil < 0 {
				return api.ErrOver
			}
		}

		size, err := io.ReadFull(io.NewSectionReader(handle, int64(index)*wanted.PieceSize, wanted.PieceSize), piece)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return e.giveUp(ctx, wanted.ID, "unreadable")
		}
		sum := sha256.Sum256(piece[:size])
		limit.wait(ctx, size)

		next, err := e.sendWithRetry(ctx, wanted.ID, index, piece[:size], hex.EncodeToString(sum[:]))
		if err != nil {
			return err
		}
		sendUntil = next
		e.mu.Lock()
		sending.Done = index + 1
		e.mu.Unlock()
	}
	return nil
}

// sendWithRetry tries a piece a few times: a connection that drops for a moment should
// not cost somebody a download that was nearly done.
func (e *Engine) sendWithRetry(ctx context.Context, transferID string, index int, piece []byte, sum string) (int, error) {
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		next, err := e.API.SendPiece(ctx, transferID, index, piece, sum)
		switch {
		case err == nil:
			return next, nil
		case errors.Is(err, api.ErrOver), errors.Is(err, api.ErrRevoked), errors.Is(err, auth.ErrSignedOut):
			return 0, err
		case errors.Is(err, api.ErrNotYet):
			last = err
			if !sleep(ctx, time.Second) {
				return 0, ctx.Err()
			}
		default:
			var problem *api.Problem
			if errors.As(err, &problem) && problem.Status < 500 {
				return 0, err
			}
			last = err
			if !sleep(ctx, time.Duration(attempt+1)*2*time.Second) {
				return 0, ctx.Err()
			}
		}
	}
	return 0, fmt.Errorf("sending piece %d: %w", index, last)
}

func (e *Engine) giveUp(ctx context.Context, transferID, reason string) error {
	// The archive knows four reasons. Having waited too long is, to it, having stopped.
	told := reason
	if told == "stalled" {
		told = "stopped"
	}
	_ = e.API.GiveUp(ctx, transferID, told)
	return errGaveUp{reason: reason}
}

// A switched off program asks again this often by itself, and no sooner than this after
// the last time however often its owner presses the button.
var (
	switchedOffEvery   = 15 * time.Minute
	switchedOffAtLeast = 10 * time.Second
)

// waitSwitchedOff waits until it is worth asking the archive again. Whoever switched the
// program off can switch it on again, so it does ask, but rarely: the answer seldom
// changes, and every installation that was ever switched off would be asking. Its owner,
// who was just told it is allowed again, does not have to wait for that: they press a
// button, and that is the only request beyond four an hour.
func (e *Engine) waitSwitchedOff(ctx context.Context) bool {
	select {
	case <-e.again: // pressed before this wait began, and already answered
	default:
	}
	soonest := time.After(switchedOffAtLeast)
	timer := time.NewTimer(switchedOffEvery)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	case <-e.again:
	}
	select {
	case <-ctx.Done():
		return false
	case <-soonest:
		return true
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// limiter keeps sending under the speed its owner set, if they set one.
type limiter struct {
	perSecond int
	started   time.Time
	sent      int
}

func newLimiter(bytesPerSecond int) *limiter {
	return &limiter{perSecond: bytesPerSecond, started: time.Now()}
}

// slowest picks the lower of two speed limits, where zero means there is none.
func slowest(a, b int) int {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	default:
		return min(a, b)
	}
}

func (l *limiter) wait(ctx context.Context, n int) {
	if l.perSecond <= 0 {
		return
	}
	l.sent += n
	ahead := time.Duration(float64(l.sent)/float64(l.perSecond)*float64(time.Second)) - time.Since(l.started)
	if ahead > 0 {
		sleep(ctx, ahead)
	}
}
