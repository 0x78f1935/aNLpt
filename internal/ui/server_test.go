package ui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/0x78f1935/aNLpt/internal/api"
	"github.com/0x78f1935/aNLpt/internal/auth"
	"github.com/0x78f1935/aNLpt/internal/config"
	"github.com/0x78f1935/aNLpt/internal/share"
)

func start(t *testing.T) (*Server, string) {
	t.Setenv("ANLPT_HOME", t.TempDir())
	store, err := config.Open()
	if err != nil {
		t.Fatal(err)
	}
	session := &auth.Session{Vault: auth.NewVault(t.TempDir()), HTTP: http.DefaultClient}
	engine := share.New(store, session, &api.Client{Session: session}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := &Server{Config: store, Session: session, Engine: engine, Version: "test"}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	address, err := server.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return server, address
}

func status(t *testing.T, client *http.Client, request *http.Request) int {
	t.Helper()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

func TestNothingAnswersWithoutTheToken(t *testing.T) {
	server, _ := start(t)
	for _, path := range []string{"/", "/api/state", "/app.js", "/?token=guessed"} {
		request, _ := http.NewRequest(http.MethodGet, server.address+path, nil)
		if got := status(t, http.DefaultClient, request); got != http.StatusForbidden {
			t.Errorf("%s answered %d to a stranger", path, got)
		}
	}
}

func TestTheTokenOpensThePageOnce(t *testing.T) {
	_, address := start(t)
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar}

	request, _ := http.NewRequest(http.MethodGet, address, nil)
	if got := status(t, browser, request); got != http.StatusOK {
		t.Fatalf("the page answered %d to its owner", got)
	}
	// From here on the cookie does it, and the token is out of the address bar.
	request, _ = http.NewRequest(http.MethodGet, strings.Split(address, "/?")[0]+"/api/state", nil)
	if got := status(t, browser, request); got != http.StatusOK {
		t.Fatalf("the api answered %d to its owner", got)
	}
}

// A page on some other site can point a browser at this port. It must get nothing, even
// if the browser brings the cookie along.
func TestAnotherSiteGetsNothing(t *testing.T) {
	server, address := start(t)
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar}
	request, _ := http.NewRequest(http.MethodGet, address, nil)
	status(t, browser, request)

	request, _ = http.NewRequest(http.MethodPost, server.address+"/api/sign-out", nil)
	request.Header.Set("Origin", "https://evil.example")
	if got := status(t, browser, request); got != http.StatusNotFound {
		t.Errorf("another origin got %d", got)
	}
	request, _ = http.NewRequest(http.MethodGet, server.address+"/api/state", nil)
	request.Host = "evil.example"
	if got := status(t, browser, request); got != http.StatusNotFound {
		t.Errorf("another host got %d", got)
	}
}

func TestAWholeDiskOrHomeIsNotAFolderToShare(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory here")
	}
	root := filepath.VolumeName(home) + string(filepath.Separator)
	for _, path := range []string{home, root} {
		if !tooWide(path) {
			t.Errorf("%q would have been shared whole", path)
		}
	}
	if tooWide(filepath.Join(home, "Videos", "Series")) {
		t.Error("an ordinary folder was refused")
	}
}

func TestAFolderInsideASharedOneIsTheSameFolder(t *testing.T) {
	parent := filepath.Join("media", "series")
	if !sameOrInside(parent, filepath.Join(parent, "Alfred J. Kwak")) || !sameOrInside(parent, parent) {
		t.Error("a folder inside a shared one was not recognised")
	}
	if sameOrInside(parent, filepath.Join("media", "series-old")) || sameOrInside(parent, "media") {
		t.Error("a folder next to a shared one was taken for one inside it")
	}
}

// Whether it may start with the computer is asked once, and "no" is an answer too.
func TestTheAutostartQuestionIsAskedOnce(t *testing.T) {
	server, address := start(t)
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar}
	request, _ := http.NewRequest(http.MethodGet, address, nil)
	status(t, browser, request)

	if server.Config.Get().AutostartAsked {
		t.Fatal("it counted as asked before anybody was")
	}
	request, _ = http.NewRequest(http.MethodPut, server.address+"/api/settings", strings.NewReader(`{"autostart": false}`))
	request.Header.Set("Content-Type", "application/json")
	if got := status(t, browser, request); got != http.StatusOK {
		t.Fatalf("answering got %d", got)
	}
	cfg := server.Config.Get()
	if !cfg.AutostartAsked || cfg.Autostart {
		t.Fatalf("after saying no: asked %v, autostart %v", cfg.AutostartAsked, cfg.Autostart)
	}

	// And it is remembered per installation: a new one has not been asked anything.
	fresh, _ := start(t)
	if fresh.Config.Get().AutostartAsked {
		t.Fatal("a new installation was not asked")
	}
}

// A folder can be in several spoken languages. The main one is not also one of the
// others, none is there twice, and saying nothing about them leaves them as they were.
func TestSeveralSpokenLanguages(t *testing.T) {
	folder := config.Folder{Language: "nl", Visibility: "members"}
	chosen := []string{"en", "nl", "de", "en"}
	if err := (&folderInput{OtherLanguages: &chosen}).apply(&folder); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(folder.OtherLanguages, ","); got != "de,en" {
		t.Fatalf("other languages are %q", got)
	}

	if err := (&folderInput{Visibility: "public"}).apply(&folder); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(folder.OtherLanguages, ","); got != "de,en" || folder.Visibility != "public" {
		t.Fatalf("changing something else left %q, %q", got, folder.Visibility)
	}

	// Making one of the others the main language takes it out of the others.
	if err := (&folderInput{Language: "en"}).apply(&folder); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(folder.OtherLanguages, ","); got != "de" {
		t.Fatalf("after English became the main language: %q", got)
	}

	none := []string{}
	if err := (&folderInput{OtherLanguages: &none}).apply(&folder); err != nil || len(folder.OtherLanguages) != 0 {
		t.Fatalf("clearing them left %v (%v)", folder.OtherLanguages, err)
	}
	bad := []string{"english"}
	if err := (&folderInput{OtherLanguages: &bad}).apply(&folder); err == nil {
		t.Fatal("a language that is not a code was taken")
	}
}

// A page of the archive is opened in the language of the program, because that is part of
// its address. Without it the archive says "not found".
func TestAPageOfTheArchiveHasItsLanguageInFront(t *testing.T) {
	for _, c := range []struct{ server, language, path, want string }{
		{"https://archive.my-dev.app", "nl", "/me/shares", "https://archive.my-dev.app/nl/me/shares"},
		{"http://localhost:53000/", "en", "/titles/new?q=De%20Familie%20Robinson", "http://localhost:53000/en/titles/new?q=De%20Familie%20Robinson"},
		{"https://archive.my-dev.app", "", "/", "https://archive.my-dev.app/nl/"},
	} {
		got := archivePage(config.Config{Server: c.server, Language: c.language}, c.path)
		if got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

// Several folders in one go: only what was said changes, only in the folders that were
// named, and a change that makes no sense touches none of them.
func TestSeveralFoldersAtOnce(t *testing.T) {
	cfg := &config.Config{Folders: []config.Folder{
		{ID: "a", Visibility: "members", Language: "nl", OtherLanguages: []string{"en"}, Downloadable: true},
		{ID: "b", Visibility: "public", Language: "en", Anonymous: true, Downloadable: true},
		{ID: "c", Visibility: "public", Language: "nl", Downloadable: true},
	}}
	hidden, no := true, false
	in := &severalInput{IDs: []string{"a", "b"}, Visibility: "private", Downloadable: &no, Anonymous: &hidden}

	changed, err := in.apply(cfg)

	if err != nil || changed != 2 {
		t.Fatalf("changed %d folders (%v)", changed, err)
	}
	for _, folder := range cfg.Folders[:2] {
		if folder.Visibility != "private" || folder.Downloadable || !folder.Anonymous {
			t.Fatalf("folder %s was left as %+v", folder.ID, folder)
		}
	}
	if a, b := cfg.Folders[0], cfg.Folders[1]; a.Language != "nl" || strings.Join(a.OtherLanguages, ",") != "en" || b.Language != "en" {
		t.Fatalf("languages nobody mentioned changed: %+v, %+v", a, b)
	}
	if c := cfg.Folders[2]; c.Visibility != "public" || c.Anonymous {
		t.Fatalf("a folder that was not named changed: %+v", c)
	}

	// Nothing said about the name leaves each folder's own choice alone.
	if _, err := (&severalInput{IDs: []string{"a", "b", "c"}, Language: "de"}).apply(cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Folders[1].Anonymous || cfg.Folders[2].Anonymous || cfg.Folders[2].Language != "de" {
		t.Fatalf("after only the language changed: %+v", cfg.Folders)
	}

	before := cfg.Folders[0]
	if _, err := (&severalInput{IDs: []string{"a"}, Language: "nl", Visibility: "whoever"}).apply(cfg); err == nil {
		t.Fatal("a choice that does not exist was taken")
	}
	if !reflect.DeepEqual(before, cfg.Folders[0]) {
		t.Fatalf("a refused change still changed something: %+v", cfg.Folders[0])
	}
}

// The page names directories and files inside a folder, and nothing outside it.
func TestOnlyPathsInsideTheFolder(t *testing.T) {
	for raw, want := range map[string]string{
		"Kwak/Seizoen 1":       "Kwak/Seizoen 1",
		`Kwak\Seizoen 1\a.mkv`: "Kwak/Seizoen 1/a.mkv",
		"Kwak/./Seizoen 1/":    "Kwak/Seizoen 1",
	} {
		if got, ok := insideFolder(raw); !ok || got != want {
			t.Errorf("%q became %q (%v), want %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"", ".", "..", "../elsewhere", "Kwak/../../elsewhere", "/etc"} {
		if got, ok := insideFolder(raw); ok {
			t.Errorf("%q was taken, as %q", raw, got)
		}
	}
}
