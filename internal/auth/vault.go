package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

const (
	keyringService = "aNLpt"
	keyringUser    = "archive-tokens"
)

// NewVault keeps tokens in the operating system's own keyring: the Windows Credential
// Manager, or the Secret Service (GNOME Keyring, KWallet) on Linux. That is where
// something that lets a program act as somebody belongs, and it is encrypted with the
// member's own login.
//
// A server without a desktop has no keyring. There the tokens go in a file only its owner
// can read, which is what ssh does with its keys and is as good as that gets.
func NewVault(dir string) Vault {
	return &vault{file: filepath.Join(dir, "tokens.json")}
}

type vault struct {
	file string
}

func (v *vault) Load() (Tokens, error) {
	raw, err := keyring.Get(keyringService, keyringUser)
	if err != nil {
		fromFile, fileErr := os.ReadFile(v.file)
		if fileErr != nil {
			return Tokens{}, errors.New("no tokens kept")
		}
		raw = string(fromFile)
	}
	var tokens Tokens
	if err := json.Unmarshal([]byte(raw), &tokens); err != nil {
		return Tokens{}, fmt.Errorf("the kept tokens cannot be read: %w", err)
	}
	return tokens, nil
}

func (v *vault) Save(tokens Tokens) error {
	raw, err := json.Marshal(tokens)
	if err != nil {
		return err
	}
	if err := keyring.Set(keyringService, keyringUser, string(raw)); err == nil {
		// In the keyring now, so not also lying around in a file.
		_ = os.Remove(v.file)
		return nil
	}
	tmp := v.file + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, v.file)
}

func (v *vault) Clear() error {
	_ = keyring.Delete(keyringService, keyringUser)
	if err := os.Remove(v.file); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
