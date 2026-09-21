// Package auth signs a member in, and keeps them signed in.
//
// The archive never sees a password typed into this program, because none is: signing in
// happens in the member's own browser, on the archive's own page, where they may already
// be signed in and where a password manager works. What comes back is a code, which is
// swapped for tokens. This is OAuth2's authorization code flow with PKCE (RFC 7636), over
// a loopback address (RFC 8252), which is the flow meant for programs like this one: there
// is no client secret, because a program anybody can download cannot keep one.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Scope is the one thing this program asks to be allowed to do.
const Scope = "tracker:share"

// ErrSignedOut means there is nobody signed in, or the archive no longer accepts them.
var ErrSignedOut = errors.New("not signed in")

// Tokens is what the archive hands over after a member agreed.
type Tokens struct {
	Access  string    `json:"access_token"`
	Refresh string    `json:"refresh_token"`
	Expires time.Time `json:"expires"`
}

// Vault keeps the tokens between runs. See vault.go.
type Vault interface {
	Load() (Tokens, error)
	Save(Tokens) error
	Clear() error
}

// Session hands out a token that works, renewing it when it is about to run out.
type Session struct {
	Server    string
	ClientID  string
	UserAgent string
	Vault     Vault
	HTTP      *http.Client

	mu     sync.Mutex
	tokens Tokens
	loaded bool
}

func (s *Session) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	if found, err := s.Vault.Load(); err == nil {
		s.tokens = found
	}
}

// SignedIn says whether there is anything to try. The archive has the last word.
func (s *Session) SignedIn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	return s.tokens.Refresh != "" || s.tokens.Access != ""
}

// Token returns an access token that is good for at least another minute.
func (s *Session) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	if s.tokens.Access != "" && time.Until(s.tokens.Expires) > time.Minute {
		return s.tokens.Access, nil
	}
	if s.tokens.Refresh == "" {
		return "", ErrSignedOut
	}
	fresh, err := s.exchange(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {s.tokens.Refresh},
		"client_id":     {s.ClientID},
	})
	if err != nil {
		var refused *RefusedError
		if errors.As(err, &refused) {
			// The refresh token is spent, revoked or expired. Nothing here can fix that;
			// the member has to sign in again.
			s.tokens = Tokens{}
			_ = s.Vault.Clear()
			return "", ErrSignedOut
		}
		return "", err
	}
	s.tokens = fresh
	if err := s.Vault.Save(fresh); err != nil {
		return "", fmt.Errorf("keeping the new tokens: %w", err)
	}
	return fresh.Access, nil
}

// Forget drops an access token the archive just refused, so the next call renews it.
func (s *Session) Forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens.Access = ""
}

// SignOut tells the archive to forget the tokens, and forgets them here whatever it says.
func (s *Session) SignOut(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	if s.tokens.Refresh != "" {
		form := url.Values{"token": {s.tokens.Refresh}, "client_id": {s.ClientID}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			s.Server+"/api/v1/oauth/revoke/", strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("User-Agent", s.UserAgent)
			if resp, err := s.HTTP.Do(req); err == nil {
				resp.Body.Close()
			}
		}
	}
	s.tokens = Tokens{}
	_ = s.Vault.Clear()
}

// RefusedError is the archive saying no, as opposed to not being reachable.
type RefusedError struct {
	Status int
	Reason string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("the archive refused (%d): %s", e.Status, e.Reason)
}

func (s *Session) exchange(ctx context.Context, form url.Values) (Tokens, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.Server+"/api/v1/oauth/token/", strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", s.UserAgent)
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return Tokens{}, fmt.Errorf("reaching the archive: %w", err)
	}
	defer resp.Body.Close()

	var body struct {
		Access      string `json:"access_token"`
		Refresh     string `json:"refresh_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil && resp.StatusCode < 400 {
		return Tokens{}, fmt.Errorf("reading the archive's answer: %w", err)
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
		return Tokens{}, &RefusedError{Status: resp.StatusCode, Reason: body.Error}
	}
	if resp.StatusCode != http.StatusOK || body.Access == "" {
		return Tokens{}, fmt.Errorf("the archive answered %d", resp.StatusCode)
	}
	return Tokens{
		Access:  body.Access,
		Refresh: body.Refresh,
		Expires: time.Now().Add(time.Duration(body.ExpiresIn) * time.Second),
	}, nil
}

// SignIn opens the archive's consent page in the member's browser and waits for them.
//
// open is given the address to show; it is a parameter so a computer without a screen
// can print the address instead of opening it.
func (s *Session) SignIn(ctx context.Context, open func(address string) error) error {
	verifier, err := random(48)
	if err != nil {
		return err
	}
	state, err := random(24)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	// Any free port, and only on this computer: nothing else on the network can reach it.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listening for the archive's answer: %w", err)
	}
	defer listener.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Addr().(*net.TCPAddr).Port)

	type answer struct {
		code string
		err  error
	}
	answered := make(chan answer, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		var got answer
		switch {
		case query.Get("state") != state:
			// Somebody else's answer, or a page that guessed the port. Not ours.
			got.err = errors.New("the answer did not belong to this sign-in")
		case query.Get("error") != "":
			got.err = fmt.Errorf("the archive said: %s", query.Get("error"))
		case query.Get("code") == "":
			got.err = errors.New("the archive sent no code")
		default:
			got.code = query.Get("code")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, donePage(got.err == nil))
		select {
		case answered <- got:
		default:
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	address := s.Server + "/oauth/authorize?" + url.Values{
		"response_type":         {"code"},
		"client_id":             {s.ClientID},
		"redirect_uri":          {redirect},
		"scope":                 {Scope},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode()
	if err := open(address); err != nil {
		return fmt.Errorf("opening the browser: %w", err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(10 * time.Minute):
		return errors.New("nobody finished signing in")
	case got := <-answered:
		if got.err != nil {
			return got.err
		}
		fresh, err := s.exchange(ctx, url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {got.code},
			"redirect_uri":  {redirect},
			"client_id":     {s.ClientID},
			"code_verifier": {verifier},
		})
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.tokens, s.loaded = fresh, true
		s.mu.Unlock()
		return s.Vault.Save(fresh)
	}
}

func random(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func donePage(ok bool) string {
	message := "Gelukt. Je kunt dit tabblad sluiten en teruggaan naar aNLpt.<br><small>Done. You can close this tab and go back to aNLpt.</small>"
	if !ok {
		message = "Inloggen is niet gelukt. Probeer het opnieuw vanuit aNLpt.<br><small>Signing in did not work. Try again from aNLpt.</small>"
	}
	return `<!doctype html><meta charset="utf-8"><title>aNLpt</title>` +
		`<body style="font-family:system-ui;max-width:32rem;margin:4rem auto;padding:0 1rem;line-height:1.5">` +
		`<h1>aNLpt</h1><p>` + message + `</p>`
}
