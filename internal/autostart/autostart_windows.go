//go:build windows

// Package autostart makes aNLpt start when its owner signs in to the computer, if they
// asked for that.
package autostart

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const valueName = "aNLpt"

// Set writes or removes the entry under the member's own Run key. It is the documented
// place for this, it is per user, it needs no administrator, and it shows up in Task
// Manager under Startup, where its owner can switch it off without opening this program.
func Set(enabled bool) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if !enabled {
		if err := key.DeleteValue(valueName); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return key.SetStringValue(valueName, `"`+exe+`" --background`)
}
