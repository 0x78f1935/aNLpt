//go:build windows

// Package browser opens an address in the member's own browser.
package browser

import (
	"golang.org/x/sys/windows"
)

// Open asks Windows to open the address with whatever the member uses for the web.
//
// Through ShellExecute, which is how a program is meant to do this. The shortcut a lot of
// programs take is starting "cmd /c start" or "rundll32", and a program that starts a
// command prompt is exactly what a virus scanner is watching for.
func Open(address string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(address)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}
