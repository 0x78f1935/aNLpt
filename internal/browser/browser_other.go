//go:build !windows

// Package browser opens an address in the member's own browser.
package browser

import (
	"errors"
	"os/exec"
)

// Open hands the address to the desktop, which knows which browser its owner uses.
func Open(address string) error {
	for _, opener := range []string{"xdg-open", "gio", "open"} {
		path, err := exec.LookPath(opener)
		if err != nil {
			continue
		}
		args := []string{address}
		if opener == "gio" {
			args = []string{"open", address}
		}
		return exec.Command(path, args...).Start()
	}
	return errors.New("no way to open a browser was found")
}
