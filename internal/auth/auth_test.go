package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

type memory struct{ tokens Tokens }

func (m *memory) Load() (Tokens, error) {
	if m.tokens.Refresh == "" && m.tokens.Access == "" {
		return Tokens{}, errors.New("empty")
	}
	return m.tokens, nil
}
func (m *memory) Save(t Tokens) error { m.tokens = t; return nil }
func (m *memory) Clear() error        { m.tokens = Tokens{}; return nil }

// archive is just enough of the archive to sign in against: it remembers the challenge
// it was shown and only hands out tokens to whoever knows what it was made from.
type archive struct {
	*httptest.Server
	challenge string
	refused   bool
	issued    int
}

func newArchive(t *testing.T) *archive {
	a := &archive{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/oauth/token/", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.UserAgent() != "aNLpt/test" {
			t.Errorf("the program introduced itself as %q", r.UserAgent())
		}
		if r.Form.Get("client_secret") != "" {
			t.Error("a secret was sent, and this program has none")
		}
		ok := false
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			ok = r.Form.Get("code") == "the-code" && base64.RawURLEncoding.EncodeToString(sum[:]) == a.challenge
		case "refresh_token":
			ok = !a.refused && r.Form.Get("refresh_token") != ""
		}
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		a.issued++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access", "refresh_token": "refresh", "expires_in": 3600,
		})
	})
	a.Server = httptest.NewServer(mux)
	t.Cleanup(a.Close)
	return a
}

func session(a *archive, vault Vault) *Session {
	return &Session{Server: a.URL, ClientID: "anlpt", UserAgent: "aNLpt/test", Vault: vault, HTTP: a.Client()}
}

// browse plays the member: it "opens" the consent page and is sent back with a code.
func browse(t *testing.T, a *archive, tamper func(url.Values)) func(string) error {
	return func(address string) error {
		parsed, err := url.Parse(address)
		if err != nil {
			return err
		}
		query := parsed.Query()
		if query.Get("code_challenge_method") != "S256" || query.Get("scope") != Scope {
			t.Errorf("asked for %v", query)
		}
		a.challenge = query.Get("code_challenge")
		back := url.Values{"code": {"the-code"}, "state": {query.Get("state")}}
		if tamper != nil {
			tamper(back)
		}
		go func() {
			resp, err := http.Get(query.Get("redirect_uri") + "?" + back.Encode())
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
}

func TestSigningIn(t *testing.T) {
	a := newArchive(t)
	vault := &memory{}
	s := session(a, vault)

	if err := s.SignIn(context.Background(), browse(t, a, nil)); err != nil {
		t.Fatal(err)
	}
	token, err := s.Token(context.Background())
	if err != nil || token != "access" {
		t.Fatalf("token %q, %v", token, err)
	}
	if vault.tokens.Refresh != "refresh" {
		t.Fatal("the tokens were not kept")
	}
}

func TestAnAnswerMeantForSomebodyElseIsRefused(t *testing.T) {
	a := newArchive(t)
	s := session(a, &memory{})
	err := s.SignIn(context.Background(), browse(t, a, func(back url.Values) { back.Set("state", "guessed") }))
	if err == nil || a.issued != 0 {
		t.Fatalf("signed in on an answer that was not ours: %v", err)
	}
}

func TestATokenIsRenewedBeforeItRunsOut(t *testing.T) {
	a := newArchive(t)
	vault := &memory{tokens: Tokens{Access: "old", Refresh: "refresh", Expires: time.Now().Add(10 * time.Second)}}
	s := session(a, vault)

	token, err := s.Token(context.Background())
	if err != nil || token != "access" || a.issued != 1 {
		t.Fatalf("token %q, issued %d, %v", token, a.issued, err)
	}
	if _, _ = s.Token(context.Background()); a.issued != 1 {
		t.Fatal("a good token was renewed anyway")
	}
}

func TestARefusalMeansSigningInAgain(t *testing.T) {
	a := newArchive(t)
	a.refused = true
	vault := &memory{tokens: Tokens{Refresh: "revoked"}}
	s := session(a, vault)

	if _, err := s.Token(context.Background()); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("got %v", err)
	}
	if s.SignedIn() || vault.tokens.Refresh != "" {
		t.Fatal("tokens the archive refused were kept")
	}
}

func TestTheArchiveBeingDownIsNotBeingSignedOut(t *testing.T) {
	a := newArchive(t)
	vault := &memory{tokens: Tokens{Refresh: "refresh"}}
	s := session(a, vault)
	a.Close()

	if _, err := s.Token(context.Background()); err == nil || errors.Is(err, ErrSignedOut) {
		t.Fatalf("got %v", err)
	}
	if vault.tokens.Refresh != "refresh" {
		t.Fatal("a member was signed out because the archive was unreachable")
	}
}
