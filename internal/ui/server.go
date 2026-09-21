// Package ui is the window of aNLpt, which is a page in the member's own browser.
//
// A page rather than a real window, so that the program stays one small file with nothing
// to install: every computer already has a browser, and a windowing library is ten times
// the size of everything else here. The page is served from this computer to this
// computer and to nothing else.
//
// Two things keep other programs and other web pages out of it:
//
//   - it listens on 127.0.0.1 only, so nothing on the network can reach it;
//   - every request has to carry a token that is made up when the program starts and only
//     ever handed to the browser this program opened. A web page somewhere else can guess
//     the port, but not the token, and without it nothing here answers.
package ui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/0x78f1935/aNLpt/internal/auth"
	"github.com/0x78f1935/aNLpt/internal/autostart"
	"github.com/0x78f1935/aNLpt/internal/browser"
	"github.com/0x78f1935/aNLpt/internal/config"
	"github.com/0x78f1935/aNLpt/internal/share"
)

//go:embed web
var web embed.FS

const cookieName = "anlpt"

// Server serves the pages and the little API behind them.
type Server struct {
	Config  *config.Store
	Session *auth.Session
	Engine  *share.Engine
	Version string
	// PickFolder shows the operating system's own "choose a folder" window, where there
	// is one. Nil means the member types the path.
	PickFolder func() (string, error)

	token   string
	address string
}

// Start begins listening and returns the address to open, token included.
func (s *Server) Start(ctx context.Context) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	s.token = base64.RawURLEncoding.EncodeToString(raw)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	s.address = "http://" + listener.Addr().String()

	static, err := fs.Sub(web, "web")
	if err != nil {
		return "", err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("POST /api/sign-in", s.signIn)
	mux.HandleFunc("POST /api/sign-out", s.signOut)
	mux.HandleFunc("POST /api/folders", s.addFolder)
	mux.HandleFunc("PUT /api/folders", s.changeFolders)
	mux.HandleFunc("PUT /api/folders/{id}", s.changeFolder)
	mux.HandleFunc("DELETE /api/folders/{id}", s.removeFolder)
	mux.HandleFunc("GET /api/folders/{id}/files", s.folderFiles)
	mux.HandleFunc("PUT /api/folders/{id}/audience", s.folderAudience)
	mux.HandleFunc("PUT /api/folders/{id}/languages", s.folderLanguages)
	mux.HandleFunc("POST /api/pick-folder", s.pickFolder)
	mux.HandleFunc("POST /api/rescan", s.rescan)
	mux.HandleFunc("POST /api/try-again", s.tryAgain)
	mux.HandleFunc("POST /api/pause", s.pause)
	mux.HandleFunc("PUT /api/settings", s.settings)
	mux.HandleFunc("POST /api/open", s.open)
	mux.Handle("/", http.FileServerFS(static))

	server := &http.Server{Handler: s.guard(mux), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	return s.URL(), nil
}

// URL is the address to open. It carries the token once; the page swaps it for a cookie.
func (s *Server) URL() string { return s.address + "/?token=" + s.token }

// guard lets a request through when it carries the token, and answers nothing otherwise.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A page on another site that points a browser here sends that site as the Host
		// or the Origin. Only this address is this program.
		if r.Host != strings.TrimPrefix(s.address, "http://") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != s.address {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if given := r.URL.Query().Get("token"); given != "" && s.matches(given) {
			http.SetCookie(w, &http.Cookie{
				Name: cookieName, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
			})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		cookie, err := r.Cookie(cookieName)
		if err != nil || !s.matches(cookie.Value) {
			http.Error(w, "Open aNLpt from its icon next to the clock.", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) matches(given string) bool {
	return subtle.ConstantTimeCompare([]byte(given), []byte(s.token)) == 1
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func fail(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]string{"error": message})
}

func (s *Server) state(w http.ResponseWriter, _ *http.Request) {
	cfg := s.Config.Get()
	reply(w, http.StatusOK, map[string]any{
		"version":         s.Version,
		"server":          cfg.Server,
		"language":        cfg.Language,
		"folders":         cfg.Folders,
		"autostart":       cfg.Autostart,
		"autostart_asked": cfg.AutostartAsked,
		"scan_minutes":    cfg.ScanMinutes,
		"upload_kbps":     cfg.UploadKBps,
		"can_pick":        s.PickFolder != nil,
		"status":          s.Engine.Status(),
	})
}

func (s *Server) signIn(w http.ResponseWriter, _ *http.Request) {
	// Not tied to this request: the member is off in another tab for as long as it takes.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 11*time.Minute)
		defer cancel()
		_ = s.Session.SignIn(ctx, browser.Open)
	}()
	reply(w, http.StatusAccepted, map[string]bool{"started": true})
}

func (s *Server) signOut(w http.ResponseWriter, r *http.Request) {
	s.Session.SignOut(r.Context())
	reply(w, http.StatusOK, map[string]bool{"ok": true})
}

type folderInput struct {
	Path       string `json:"path"`
	Visibility string `json:"visibility"`
	Anonymous  bool   `json:"anonymous"`
	Language   string `json:"language"`
	// OtherLanguages is nil when the page did not say, and empty when it said none.
	OtherLanguages *[]string `json:"other_languages"`
	Downloadable   *bool     `json:"downloadable"`
}

var visibilities = map[string]bool{"public": true, "members": true, "private": true}

func (in *folderInput) apply(folder *config.Folder) error {
	if in.Visibility != "" {
		if !visibilities[in.Visibility] {
			return fmt.Errorf("unknown visibility %q", in.Visibility)
		}
		folder.Visibility = in.Visibility
	}
	if in.Language != "" {
		if len(in.Language) != 2 {
			return fmt.Errorf("unknown language %q", in.Language)
		}
		folder.Language = in.Language
	}
	if in.OtherLanguages != nil {
		folder.OtherLanguages = nil
		for _, code := range *in.OtherLanguages {
			if len(code) != 2 {
				return fmt.Errorf("unknown language %q", code)
			}
			folder.OtherLanguages = append(folder.OtherLanguages, code)
		}
	}
	// The main language is not also one of the others, and none is there twice.
	folder.OtherLanguages = without(folder.OtherLanguages, folder.Language)
	folder.Anonymous = in.Anonymous
	if in.Downloadable != nil {
		folder.Downloadable = *in.Downloadable
	}
	return nil
}

func (s *Server) addFolder(w http.ResponseWriter, r *http.Request) {
	var in folderInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "That could not be read.")
		return
	}
	path, err := filepath.Abs(strings.TrimSpace(in.Path))
	if err != nil || strings.TrimSpace(in.Path) == "" {
		fail(w, http.StatusBadRequest, "folder_missing")
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		fail(w, http.StatusBadRequest, "folder_missing")
		return
	}
	// Sharing a whole disk or a whole home directory is never what somebody meant, and it
	// is the one mistake here that cannot be taken back once a file has been fetched.
	if tooWide(path) {
		fail(w, http.StatusBadRequest, "folder_too_wide")
		return
	}
	// With members only, in Dutch, and there for others to fetch. A program that reads
	// whole folders off a disk starts out showing what it finds to the people who are in
	// on it; showing it to every visitor is its owner's choice, one button away.
	folder := config.Folder{
		ID: config.NewID(), Path: path, Visibility: "members", Language: "nl", Downloadable: true,
	}
	// What the member chose on their profile about their name next to their copies is
	// how a folder starts out: somebody who said "not my name" once should not have to say
	// it again for every folder they share. The page may still say otherwise.
	if member := s.Engine.Member(); member != nil {
		in.Anonymous = in.Anonymous || !member.ShowHolderName
	}
	if err := in.apply(&folder); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	duplicate := false
	err = s.Config.Update(func(cfg *config.Config) {
		for _, existing := range cfg.Folders {
			if sameOrInside(existing.Path, path) || sameOrInside(path, existing.Path) {
				duplicate = true
				return
			}
		}
		cfg.Folders = append(cfg.Folders, folder)
	})
	if duplicate {
		fail(w, http.StatusConflict, "folder_overlaps")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Engine.SettingsChanged()
	reply(w, http.StatusCreated, folder)
}

func (s *Server) changeFolder(w http.ResponseWriter, r *http.Request) {
	var in folderInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "That could not be read.")
		return
	}
	id := r.PathValue("id")
	var problem error
	found := false
	err := s.Config.Update(func(cfg *config.Config) {
		for i := range cfg.Folders {
			if cfg.Folders[i].ID == id {
				found = true
				problem = in.apply(&cfg.Folders[i])
			}
		}
	})
	switch {
	case !found:
		fail(w, http.StatusNotFound, "No such folder.")
	case problem != nil:
		fail(w, http.StatusBadRequest, problem.Error())
	case err != nil:
		fail(w, http.StatusInternalServerError, err.Error())
	default:
		s.Engine.SettingsChanged()
		reply(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// severalInput changes several folders in one go. Whatever it does not mention stays
// the way each folder had it: somebody with forty folders who wants them all shown without
// their name does not want their languages made the same along the way.
type severalInput struct {
	IDs            []string  `json:"ids"`
	Visibility     string    `json:"visibility"`
	Downloadable   *bool     `json:"downloadable"`
	Anonymous      *bool     `json:"anonymous"`
	Language       string    `json:"language"`
	OtherLanguages *[]string `json:"other_languages"`
}

func (in *severalInput) forFolder(folder config.Folder) *folderInput {
	one := &folderInput{
		Visibility: in.Visibility, Downloadable: in.Downloadable, Anonymous: folder.Anonymous,
		Language: in.Language, OtherLanguages: in.OtherLanguages,
	}
	if in.Anonymous != nil {
		one.Anonymous = *in.Anonymous
	}
	return one
}

// apply changes the folders that were named and says how many that was. All of them or
// none: what is wrong with the change is found out before any folder is touched.
func (in *severalInput) apply(cfg *config.Config) (int, error) {
	var scratch config.Folder
	if err := in.forFolder(scratch).apply(&scratch); err != nil {
		return 0, err
	}
	named := make(map[string]bool, len(in.IDs))
	for _, id := range in.IDs {
		named[id] = true
	}
	changed := 0
	for i := range cfg.Folders {
		if !named[cfg.Folders[i].ID] {
			continue
		}
		if err := in.forFolder(cfg.Folders[i]).apply(&cfg.Folders[i]); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

func (s *Server) changeFolders(w http.ResponseWriter, r *http.Request) {
	var in severalInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024)).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "That could not be read.")
		return
	}
	var problem error
	changed := 0
	err := s.Config.Update(func(cfg *config.Config) {
		changed, problem = in.apply(cfg)
	})
	switch {
	case problem != nil:
		fail(w, http.StatusBadRequest, problem.Error())
	case err != nil:
		fail(w, http.StatusInternalServerError, err.Error())
	default:
		if changed > 0 {
			s.Engine.SettingsChanged()
		}
		reply(w, http.StatusOK, map[string]int{"changed": changed})
	}
}

// folderFiles lists what a shared folder holds, for the page to draw as a tree. Paths
// from the folder down and sizes: the page runs on this computer and shows them to the
// person whose files they are.
func (s *Server) folderFiles(w http.ResponseWriter, r *http.Request) {
	type listed struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	files := []listed{}
	for _, file := range s.Engine.Files(r.PathValue("id")) {
		files = append(files, listed{Path: file.Path, Size: file.Size})
	}
	reply(w, http.StatusOK, map[string]any{"files": files})
}

// insideFolder tidies a path the page sent and says whether it stays inside the folder.
func insideFolder(raw string) (string, bool) {
	cleaned := path.Clean(strings.ReplaceAll(raw, "\\", "/"))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "/") || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return cleaned, true
}

// insideInput is something said about some of the directories and files in a folder.
type insideInput struct {
	Paths          []string `json:"paths"`
	Visibility     string   `json:"visibility"`
	Language       string   `json:"language"`
	OtherLanguages []string `json:"other_languages"`
}

// folderLanguages says what some of the series in a folder are spoken in. A series is
// what lies directly in the shared folder: the archive keeps languages per copy, so
// a season or an episode has none of its own to set.
func (s *Server) folderLanguages(w http.ResponseWriter, r *http.Request) {
	s.changeInside(w, r, func(in *insideInput, paths []string) (func(*config.Folder), error) {
		for _, code := range append([]string{in.Language}, in.OtherLanguages...) {
			if len(code) != 2 && (code != "" || len(in.OtherLanguages) > 0) {
				return nil, fmt.Errorf("unknown language %q", code)
			}
		}
		for _, inside := range paths {
			if strings.Contains(inside, "/") {
				return nil, fmt.Errorf("languages are set per series")
			}
		}
		return func(folder *config.Folder) { folder.SetLanguages(paths, in.Language, in.OtherLanguages) }, nil
	})
}

// folderAudience says who may fetch some of the directories and files in a folder.
func (s *Server) folderAudience(w http.ResponseWriter, r *http.Request) {
	s.changeInside(w, r, func(in *insideInput, paths []string) (func(*config.Folder), error) {
		if in.Visibility != "" && !visibilities[in.Visibility] {
			return nil, fmt.Errorf("unknown visibility %q", in.Visibility)
		}
		return func(folder *config.Folder) { folder.SetAudience(paths, in.Visibility) }, nil
	})
}

// changeInside reads what the page said, lets check decide what to do with it, and does
// that to the folder.
func (s *Server) changeInside(w http.ResponseWriter, r *http.Request, check func(*insideInput, []string) (func(*config.Folder), error)) {
	var in insideInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024*1024)).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "That could not be read.")
		return
	}
	paths := make([]string, 0, len(in.Paths))
	for _, raw := range in.Paths {
		cleaned, ok := insideFolder(raw)
		if !ok {
			fail(w, http.StatusBadRequest, "That is not inside the folder.")
			return
		}
		paths = append(paths, cleaned)
	}
	apply, problem := check(&in, paths)
	if problem != nil {
		fail(w, http.StatusBadRequest, problem.Error())
		return
	}
	id := r.PathValue("id")
	var rules []config.Rule
	found := false
	err := s.Config.Update(func(cfg *config.Config) {
		for i := range cfg.Folders {
			if cfg.Folders[i].ID == id {
				found = true
				apply(&cfg.Folders[i])
				rules = cfg.Folders[i].Rules
			}
		}
	})
	switch {
	case !found:
		fail(w, http.StatusNotFound, "No such folder.")
	case err != nil:
		fail(w, http.StatusInternalServerError, err.Error())
	default:
		s.Engine.SettingsChanged()
		if rules == nil {
			rules = []config.Rule{}
		}
		reply(w, http.StatusOK, map[string]any{"rules": rules})
	}
}

func (s *Server) removeFolder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.Config.Update(func(cfg *config.Config) {
		kept := cfg.Folders[:0]
		for _, folder := range cfg.Folders {
			if folder.ID != id {
				kept = append(kept, folder)
			}
		}
		cfg.Folders = kept
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Engine.SettingsChanged()
	reply(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) pickFolder(w http.ResponseWriter, _ *http.Request) {
	if s.PickFolder == nil {
		fail(w, http.StatusNotImplemented, "Type the path instead.")
		return
	}
	path, err := s.PickFolder()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	reply(w, http.StatusOK, map[string]string{"path": path})
}

func (s *Server) rescan(w http.ResponseWriter, _ *http.Request) {
	s.Engine.Rescan()
	reply(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (s *Server) tryAgain(w http.ResponseWriter, _ *http.Request) {
	s.Engine.TryAgain()
	reply(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (s *Server) pause(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paused bool `json:"paused"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in)
	s.Engine.Pause(in.Paused)
	reply(w, http.StatusOK, map[string]bool{"paused": in.Paused})
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Language    *string `json:"language"`
		Autostart   *bool   `json:"autostart"`
		ScanMinutes *int    `json:"scan_minutes"`
		UploadKBps  *int    `json:"upload_kbps"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "That could not be read.")
		return
	}
	if in.Autostart != nil {
		if err := autostart.Set(*in.Autostart); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	err := s.Config.Update(func(cfg *config.Config) {
		if in.Language != nil && (*in.Language == "nl" || *in.Language == "en") {
			cfg.Language = *in.Language
			cfg.LanguageChosen = true
		}
		if in.Autostart != nil {
			cfg.Autostart = *in.Autostart
			// Yes or no, it is an answer, and the question is not put again.
			cfg.AutostartAsked = true
		}
		if in.ScanMinutes != nil && *in.ScanMinutes >= 5 && *in.ScanMinutes <= 24*60 {
			cfg.ScanMinutes = *in.ScanMinutes
		}
		if in.UploadKBps != nil && *in.UploadKBps >= 0 {
			cfg.UploadKBps = *in.UploadKBps
		}
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	reply(w, http.StatusOK, map[string]bool{"ok": true})
}

// open shows a page of the archive in the browser. Only ever the archive: the page asks
// for a path, and the address it is put behind is the one in the configuration.
func (s *Server) open(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in)
	if !strings.HasPrefix(in.Path, "/") || strings.HasPrefix(in.Path, "//") {
		fail(w, http.StatusBadRequest, "Only pages of the archive.")
		return
	}
	if err := browser.Open(archivePage(s.Config.Get(), in.Path)); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	reply(w, http.StatusOK, map[string]bool{"ok": true})
}

// without returns the codes in order, once each, leaving one of them out.
func without(codes []string, leftOut string) []string {
	seen := map[string]bool{leftOut: true}
	var kept []string
	for _, code := range codes {
		if !seen[code] {
			seen[code] = true
			kept = append(kept, code)
		}
	}
	sort.Strings(kept)
	return kept
}

// archivePage is the address of a page of the archive. Every one of them has its language
// in front, /nl/me/shares, and without it the archive answers "not found": that is where
// the button under "not recognised" used to lead.
func archivePage(cfg config.Config, path string) string {
	language := cfg.Language
	if language != "en" {
		language = "nl"
	}
	return strings.TrimRight(cfg.Server, "/") + "/" + language + path
}

func sameOrInside(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// tooWide says whether a folder is the root of a disk or somebody's whole home directory.
func tooWide(path string) bool {
	clean := filepath.Clean(path)
	if filepath.Dir(clean) == clean {
		return true
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(home) == clean {
		return true
	}
	return false
}
