// Package config keeps what aNLpt has to remember between runs: where the archive is,
// which computer this is, and which folders its owner chose to share.
//
// Nothing in here is a secret. The sign-in tokens are kept elsewhere (package auth), in
// the operating system's own keyring, so this file can be read, copied and backed up
// without handing anybody an account.
package config

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// DefaultServer is the archive this program was made for. It can be pointed elsewhere
// with -server or ANLPT_SERVER, which is how it is run against a development copy.
const DefaultServer = "https://archive.my-dev.app"

// ClientID is how the archive knows this program. It is public on purpose: a desktop
// program cannot keep a secret, so it has none, and signs in with PKCE instead.
const ClientID = "anlpt"

// Folder is one folder its owner chose to share, and how.
type Folder struct {
	// ID is made up here and never changes, so a folder can be renamed or moved and
	// still be the same folder to the archive.
	ID string `json:"id"`
	// Path is where it is on this computer. It never leaves this computer: the archive
	// is only ever told the folder's name and the paths inside it.
	Path string `json:"path"`
	// Visibility is who may see the copies found in here: public, members or private.
	Visibility string `json:"visibility"`
	// Anonymous keeps the owner's name off those copies.
	Anonymous bool `json:"anonymous"`
	// Language is the main spoken language of what is in here, as an ISO 639-1 code.
	Language string `json:"language"`
	// OtherLanguages are further spoken tracks on the same files: a recording with a
	// Dutch and an English track is one copy that is both.
	OtherLanguages []string `json:"other_languages"`
	// Downloadable says whether other members may fetch these files. Off means they
	// count for the library and for XP and stay where they are.
	Downloadable bool `json:"downloadable"`
	// Rules say otherwise for something inside the folder: a series, a season, one
	// episode. Most folders have none.
	Rules []Rule `json:"rules,omitempty"`
}

// Rule says something about one directory or one file inside a shared folder that is not
// what the folder says: who may fetch it, what it is spoken in, or both.
type Rule struct {
	// Path is from the shared folder down, with forward slashes, like a file's.
	Path string `json:"path"`
	// Visibility is public, members or private, the same three words as a folder has,
	// or empty where this rule is not about that.
	Visibility string `json:"visibility,omitempty"`
	// Language and OtherLanguages are what it is spoken in, as a folder has them, or
	// empty where this rule is not about that. They are set on a series: the archive
	// keeps languages per copy, and an episode has none of its own.
	Language       string   `json:"language,omitempty"`
	OtherLanguages []string `json:"other_languages,omitempty"`
}

func (r Rule) empty() bool { return r.Visibility == "" && r.Language == "" }

// within says whether path is inside, or is, what a rule is about.
func within(path, rule string) bool {
	return path == rule || strings.HasPrefix(path, rule+"/")
}

// deepest finds the rule that goes for path among those that say anything about what is
// being asked. The deepest one wins: a season kept back in a series that is given away
// stays kept back.
func (f Folder) deepest(path string, says func(Rule) bool) (Rule, bool) {
	best, found := -1, Rule{}
	for _, rule := range f.Rules {
		if says(rule) && within(path, rule.Path) && len(rule.Path) > best {
			best, found = len(rule.Path), rule
		}
	}
	return found, best >= 0
}

// AudienceOf is who may fetch the file at path, where something inside the folder says
// so, and "" where the folder's own choice goes.
func (f Folder) AudienceOf(path string) string {
	rule, _ := f.deepest(path, func(r Rule) bool { return r.Visibility != "" })
	return rule.Visibility
}

// LanguagesOf is what the file at path is spoken in, where something inside the folder
// says so, and "" where the folder's own languages go.
func (f Folder) LanguagesOf(path string) (string, []string) {
	rule, _ := f.deepest(path, func(r Rule) bool { return r.Language != "" })
	return rule.Language, rule.OtherLanguages
}

// set changes one side of the rules for these paths and everything in them: what was
// said before about that side of anything inside is replaced, because somebody who sets
// a whole series means the whole series. The other side of a rule is left as it was.
func (f *Folder) set(paths []string, clear func(*Rule), write func(*Rule) bool) {
	kept := f.Rules[:0:0]
	for _, rule := range f.Rules {
		for _, path := range paths {
			if within(rule.Path, path) {
				clear(&rule)
			}
		}
		if !rule.empty() {
			kept = append(kept, rule)
		}
	}
	for _, path := range paths {
		at := -1
		for i := range kept {
			if kept[i].Path == path {
				at = i
			}
		}
		if at < 0 {
			kept = append(kept, Rule{Path: path})
			at = len(kept) - 1
		}
		if !write(&kept[at]) || kept[at].empty() {
			kept = append(kept[:at], kept[at+1:]...)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Path < kept[j].Path })
	f.Rules = kept
}

// SetAudience says who may fetch each of these directories and files, and everything in
// them. "" takes the say away again, and what is above it goes.
func (f *Folder) SetAudience(paths []string, visibility string) {
	f.set(paths,
		func(rule *Rule) { rule.Visibility = "" },
		func(rule *Rule) bool { rule.Visibility = visibility; return true })
}

// SetLanguages says what each of these is spoken in. "" as the main language takes the
// say away again, and the folder's own languages go.
func (f *Folder) SetLanguages(paths []string, language string, others []string) {
	f.set(paths,
		func(rule *Rule) { rule.Language, rule.OtherLanguages = "", nil },
		func(rule *Rule) bool {
			rule.Language, rule.OtherLanguages = language, nil
			for _, code := range others {
				if code != language && language != "" {
					rule.OtherLanguages = append(rule.OtherLanguages, code)
				}
			}
			return true
		})
}

// Config is the whole of it.
type Config struct {
	Server   string `json:"server"`
	DeviceID string `json:"device_id"`
	ClientID string `json:"client_id,omitempty"` // the archive's id for this installation
	Language string `json:"language"`            // of this program's own pages: nl or en
	// LanguageChosen says whether its owner picked that language here. Until they do,
	// the program follows the language they read the archive in.
	LanguageChosen bool     `json:"language_chosen"`
	Folders        []Folder `json:"folders"`
	// ScanMinutes is how often the shared folders are looked through again.
	ScanMinutes int `json:"scan_minutes"`
	// Autostart says whether the program starts when its owner signs in to the computer.
	Autostart bool `json:"autostart"`
	// AutostartAsked says whether its owner was asked about that yet. A program that
	// starts itself with the computer without having asked is exactly what people
	// distrust, and one that never offers is one they have to remember to start. So
	// it asks, once, and either answer is remembered.
	AutostartAsked bool `json:"autostart_asked"`
	// UploadKBps caps how fast files are sent, in kilobytes a second. Zero is no cap
	// here; the archive paces what it passes on either way.
	UploadKBps int `json:"upload_kbps"`
}

// Store reads and writes the configuration file, safely from several goroutines.
type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

// Dir is where aNLpt keeps its files: %AppData%\aNLpt, or ~/.config/aNLpt.
func Dir() (string, error) {
	if custom := os.Getenv("ANLPT_HOME"); custom != "" {
		return custom, os.MkdirAll(custom, 0o700)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("finding the configuration directory: %w", err)
	}
	dir := filepath.Join(base, "aNLpt")
	return dir, os.MkdirAll(dir, 0o700)
}

// Open reads the configuration, making a fresh one the first time.
func Open() (*Store, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	store := &Store{path: filepath.Join(dir, "config.json")}
	raw, err := os.ReadFile(store.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// First run.
	case err != nil:
		return nil, fmt.Errorf("reading %s: %w", store.path, err)
	default:
		if err := json.Unmarshal(raw, &store.cfg); err != nil {
			return nil, fmt.Errorf("%s is not valid: %w", store.path, err)
		}
	}
	changed := store.fillDefaults()
	if changed {
		if err := store.save(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (s *Store) fillDefaults() bool {
	changed := false
	if s.cfg.Server == "" {
		s.cfg.Server, changed = DefaultServer, true
	}
	if s.cfg.DeviceID == "" {
		s.cfg.DeviceID, changed = NewID(), true
	}
	if s.cfg.Language == "" {
		s.cfg.Language, changed = "nl", true
	}
	if s.cfg.ScanMinutes < 5 {
		s.cfg.ScanMinutes, changed = 30, true
	}
	return changed
}

// Get returns a copy, so nobody changes the configuration without going through Update.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg := s.cfg
	cfg.Folders = append([]Folder(nil), s.cfg.Folders...)
	return cfg
}

// Update changes the configuration and writes it down.
func (s *Store) Update(change func(*Config)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(&s.cfg)
	return s.save()
}

// save writes to a temporary file and renames it, so a power cut halfway through leaves
// the old configuration rather than half of a new one.
func (s *Store) save() error {
	raw, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	return os.Rename(tmp, s.path)
}

// NewID makes a random version 4 UUID.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("no randomness available: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
