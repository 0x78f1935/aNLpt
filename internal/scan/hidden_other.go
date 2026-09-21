//go:build !windows

package scan

import "strings"

// hidden says whether a name starts with a dot, which is all "hidden" means here.
func hidden(_, name string) bool {
	return strings.HasPrefix(name, ".")
}
