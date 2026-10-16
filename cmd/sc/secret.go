package main

import (
	"fmt"
	"path/filepath"

	"smartconfig/internal/check"
	"smartconfig/internal/redact"
	"smartconfig/internal/scope"
)

// secretFile reports whether p is a secret file (the scope's secret
// lines): sc stores its content but shows its secret values hidden. Like
// the store's FingerprintOnly, it asks of p as given and of p with its
// directories resolved. The tests point it elsewhere.
var secretFile = func(p string) bool {
	if scope.Default().Secret(p) {
		return true
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(p))
	return err == nil && scope.Default().Secret(filepath.Join(dir, filepath.Base(p)))
}

// hideSecrets returns data, p's content, with its secret values replaced
// by hide's markers when p is a secret file, and how many it hid.
func hideSecrets(p string, data []byte, hide redact.Hider) ([]byte, int) {
	if !secretFile(p) {
		return data, 0
	}
	return redact.File(p, data, hide)
}

// hiddenNote says on stderr that the secret values of p are shown as
// marker.
func hiddenNote(p, marker string) {
	fmt.Fprintf(noteOut, "sc: note: the secret values of %s are shown as %s\n", show(p), marker)
}

// hideSaid hides, when p is a secret file, its secret values (data is the
// content that was checked) in what the validators said about it: the
// findings' raw lines and the words behind the notes. A validator may
// quote the line it stopped at.
func hideSaid(p string, data []byte, fs []check.Finding, said []string) {
	if !secretFile(p) {
		return
	}
	vals := redact.Values(p, data)
	for i := range fs {
		if fs[i].Raw != "" {
			fs[i].Raw = redact.Line(fs[i].Raw, vals, redact.Plain)
		}
	}
	for i := range said {
		said[i] = redact.Line(said[i], vals, redact.Plain)
	}
}
