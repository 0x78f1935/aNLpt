//go:build windows

package scan

import (
	"strings"
	"syscall"
)

// hidden says whether Windows marks a file as hidden or as part of the system, or its
// name starts with a dot the way it would anywhere else.
func hidden(path, name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$") {
		return true
	}
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attributes, err := syscall.GetFileAttributes(pointer)
	if err != nil {
		return false
	}
	return attributes&(syscall.FILE_ATTRIBUTE_HIDDEN|syscall.FILE_ATTRIBUTE_SYSTEM) != 0
}
