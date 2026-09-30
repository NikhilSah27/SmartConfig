package scope

import (
	"path"
	"strings"
)

// noLogin holds the shells (by base name) of accounts that cannot log in.
var noLogin = map[string]bool{
	"nologin": true, "false": true, "sync": true, "halt": true, "shutdown": true,
}

// LoginHomes returns the home directory of each account in passwd (the
// bytes of /etc/passwd) that can log in, in file order. Accounts whose
// shell's base name is nologin, false, sync, halt or shutdown are skipped
// (an empty shell means /bin/sh, so it counts), as are malformed lines,
// relative or empty homes and duplicates. Homes are cleaned.
func LoginHomes(passwd []byte) []string {
	var homes []string
	seen := map[string]bool{}
	for _, l := range strings.Split(string(passwd), "\n") {
		f := strings.Split(l, ":")
		if len(f) != 7 || strings.HasPrefix(l, "#") {
			continue
		}
		home, shell := f[5], strings.TrimSpace(f[6])
		if noLogin[path.Base(shell)] || !strings.HasPrefix(home, "/") {
			continue
		}
		home = path.Clean(home)
		if !seen[home] {
			seen[home] = true
			homes = append(homes, home)
		}
	}
	return homes
}
