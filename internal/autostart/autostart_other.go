//go:build !windows

// Package autostart makes aNLpt start when its owner signs in to the computer, if they
// asked for that.
package autostart

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Set writes or removes a desktop entry in ~/.config/autostart, which is what every
// Linux desktop reads (the XDG autostart specification). A server without a desktop has
// no such thing; there a systemd user unit does the job, and the README has one.
func Set(enabled bool) error {
	base, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	file := filepath.Join(base, "autostart", "anlpt.desktop")
	if !enabled {
		if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	entry := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=aNLpt\nExec=%q --background\nTerminal=false\nX-GNOME-Autostart-enabled=true\n", exe)
	return os.WriteFile(file, []byte(entry), 0o644)
}
