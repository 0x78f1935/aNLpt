// aNLpt shares folders from your own computer with Archive NL.
//
// Start it and it puts an icon next to the clock and opens a page in your browser. Sign in,
// pick the folders to share, and leave it running.
//
//	anlpt                 start, and open the page
//	anlpt --background    start without opening the page (what start-at-login uses)
//	anlpt --headless      no icon and no browser: for a server. Prints the address to
//	                      open, and the address to sign in at.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ncruces/zenity"

	"github.com/0x78f1935/aNLpt/internal/api"
	"github.com/0x78f1935/aNLpt/internal/auth"
	"github.com/0x78f1935/aNLpt/internal/browser"
	"github.com/0x78f1935/aNLpt/internal/config"
	"github.com/0x78f1935/aNLpt/internal/share"
	"github.com/0x78f1935/aNLpt/internal/tray"
	"github.com/0x78f1935/aNLpt/internal/ui"
)

// version is set when a release is built (-ldflags "-X main.version=1.2.3").
var version = "dev"

func main() {
	background := flag.Bool("background", false, "start without opening the page")
	headless := flag.Bool("headless", false, "no tray icon and no browser, for a server")
	server := flag.String("server", os.Getenv("ANLPT_SERVER"), "address of the archive, for development")
	showVersion := flag.Bool("version", false, "print the version and stop")
	flag.Parse()
	if *showVersion {
		fmt.Println("aNLpt", version)
		return
	}
	if err := run(*background, *headless, *server); err != nil {
		fmt.Fprintln(os.Stderr, "aNLpt:", err)
		os.Exit(1)
	}
}

func run(background, headless bool, server string) error {
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	// One at a time. A second start only brings the first one's page to the front, which
	// is what somebody double-clicking the program again wants.
	if running := otherInstance(dir); running != "" {
		if headless {
			fmt.Println("aNLpt is already running:", running)
			return nil
		}
		return browser.Open(running)
	}

	log, closeLog := newLog(dir, headless)
	defer closeLog()

	store, err := config.Open()
	if err != nil {
		return err
	}
	if server != "" {
		if _, err := url.ParseRequestURI(server); err != nil {
			return fmt.Errorf("-server is not an address: %w", err)
		}
		if err := store.Update(func(cfg *config.Config) { cfg.Server = server }); err != nil {
			return err
		}
	}
	cfg := store.Get()

	// Who this is, said on every request. The archive's front door turns away programs
	// that do not introduce themselves.
	userAgent := "aNLpt/" + version
	client := &http.Client{Timeout: 5 * time.Minute}
	session := &auth.Session{
		Server: cfg.Server, ClientID: config.ClientID, UserAgent: userAgent,
		Vault: auth.NewVault(dir), HTTP: client,
	}
	archive := &api.Client{Server: cfg.Server, UserAgent: userAgent, Session: session, HTTP: client}
	engine := share.New(store, session, archive, version, log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pages := &ui.Server{Config: store, Session: session, Engine: engine, Version: version}
	if !headless {
		pages.PickFolder = func() (string, error) {
			path, err := zenity.SelectFile(zenity.Directory(), zenity.Title("aNLpt"))
			if err == zenity.ErrCanceled {
				return "", nil
			}
			return path, err
		}
	}
	address, err := pages.Start(ctx)
	if err != nil {
		return err
	}
	rememberInstance(dir, address)
	defer forgetInstance(dir)

	go engine.Run(ctx)
	log.Info("started", "version", version, "server", cfg.Server)

	if headless {
		fmt.Println("aNLpt is running. Its page:", address)
		if !session.SignedIn() {
			err := session.SignIn(ctx, func(signInAt string) error {
				fmt.Println("Sign in by opening this address in a browser on this computer:")
				fmt.Println(signInAt)
				return nil
			})
			if err != nil {
				return err
			}
			fmt.Println("Signed in.")
		}
		<-ctx.Done()
		return nil
	}

	if !background {
		_ = browser.Open(address)
	}
	tick := make(chan struct{})
	go func() {
		defer close(tick)
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				select {
				case tick <- struct{}{}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	tray.Run(tray.Options{
		Texts:   func() tray.Texts { return trayTexts(store.Get().Language) },
		Tooltip: func() string { return tooltip(store.Get().Language, engine.Status()) },
		Paused:  func() bool { return engine.Status().State == share.Paused },
		OnOpen:  func() { _ = browser.Open(pages.URL()) },
		OnPause: engine.Pause,
		OnQuit:  stop,
		Tick:    tick,
	})
	return nil
}

func trayTexts(language string) tray.Texts {
	if language == "en" {
		return tray.Texts{Open: "Open aNLpt", Pause: "Pause sharing", Resume: "Resume sharing", Quit: "Quit"}
	}
	return tray.Texts{Open: "aNLpt openen", Pause: "Delen pauzeren", Resume: "Delen hervatten", Quit: "Afsluiten"}
}

func tooltip(language string, status share.Status) string {
	words := map[share.State][2]string{
		share.SignedOut: {"niet ingelogd", "signed out"},
		share.Starting:  {"verbinden", "connecting"},
		share.Sharing:   {"deelt", "sharing"},
		share.Offline:   {"archief onbereikbaar", "archive unreachable"},
		share.Revoked:   {"uitgeschakeld", "switched off"},
		share.Paused:    {"gepauzeerd", "paused"},
	}
	index := 0
	if language == "en" {
		index = 1
	}
	text := "aNLpt: " + words[status.State][index]
	if status.State == share.Sharing && status.Files > 0 {
		noun := [2]string{"bestanden", "files"}[index]
		text += fmt.Sprintf(" (%d %s)", status.Files, noun)
	}
	return text
}

// newLog writes to a file next to the configuration, and to the terminal when there is one.
func newLog(dir string, alsoTerminal bool) (*slog.Logger, func()) {
	path := filepath.Join(dir, "anlpt.log")
	// One file that is started again when it gets large: nobody wants a program they
	// forgot about to have filled their disk with its diary.
	if info, err := os.Stat(path); err == nil && info.Size() > 2*1024*1024 {
		_ = os.Rename(path, path+".old")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return slog.New(slog.NewTextHandler(os.Stderr, nil)), func() {}
	}
	var out io.Writer = file
	if alsoTerminal {
		out = io.MultiWriter(file, os.Stderr)
	}
	return slog.New(slog.NewTextHandler(out, nil)), func() { _ = file.Close() }
}

type instance struct {
	Address string `json:"address"`
}

func instanceFile(dir string) string { return filepath.Join(dir, "running.json") }

// otherInstance returns the page of an aNLpt that is already running, or "".
func otherInstance(dir string) string {
	raw, err := os.ReadFile(instanceFile(dir))
	if err != nil {
		return ""
	}
	var found instance
	if json.Unmarshal(raw, &found) != nil {
		return ""
	}
	parsed, err := url.Parse(found.Address)
	if err != nil {
		return ""
	}
	// The file outlives a program that was killed, so ask whether anybody is home.
	conn, err := net.DialTimeout("tcp", parsed.Host, time.Second)
	if err != nil {
		return ""
	}
	_ = conn.Close()
	return found.Address
}

func rememberInstance(dir, address string) {
	raw, _ := json.Marshal(instance{Address: address})
	// Only its owner may read it: the address carries the token that opens the page.
	_ = os.WriteFile(instanceFile(dir), raw, 0o600)
}

func forgetInstance(dir string) { _ = os.Remove(instanceFile(dir)) }
