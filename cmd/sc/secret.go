package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"smartconfig/internal/check"
	"smartconfig/internal/redact"
	"smartconfig/internal/scope"
)

// showSecrets is set by --show-secrets (sc cat, sc diff and sc check):
// secret files are shown as stored. Only root may set it (chunk K, step
// 7). Each run builds its commands afresh, which sets it back to false.
var showSecrets bool

// geteuid is os.Geteuid; the tests point it elsewhere.
var geteuid = os.Geteuid

// addShowSecrets gives cmd the --show-secrets flag. Set by anyone but
// root, it is an error before cmd reads anything.
func addShowSecrets(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&showSecrets, "show-secrets", false, "show the secret values of secret files as stored (root only)")
	cmd.PreRunE = func(*cobra.Command, []string) error {
		if showSecrets && geteuid() != 0 {
			return errors.New("--show-secrets needs root")
		}
		return nil
	}
}

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
// by hide's markers when p is a secret file and --show-secrets is not
// set, and how many it hid.
func hideSecrets(p string, data []byte, hide redact.Hider) ([]byte, int) {
	if showSecrets || !secretFile(p) {
		return data, 0
	}
	return redact.File(p, data, hide)
}

// hiddenNote says on stderr that the secret values of p are shown as
// marker, and how to see them.
func hiddenNote(p, marker string) {
	fmt.Fprintf(noteOut, "sc: note: the secret values of %s are shown as %s (as root, --show-secrets shows them)\n", show(p), marker)
}

// hideSaid hides, when p is a secret file, its secret values (data is the
// content that was checked) in what the validators said about it: the
// findings' raw lines and the words behind the notes. A validator may
// quote the line it stopped at. --show-secrets leaves them as said.
func hideSaid(p string, data []byte, fs []check.Finding, said []string) {
	if showSecrets || !secretFile(p) {
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
