// Package check finds problems in config files (M3 plan 5). Each checker
// runs the consumer's own validator in its check-only form on a scratch
// copy and adds rules of ours. It knows nothing of the store or the
// watcher: it is given a path and the bytes to check, and returns findings.
package check

// Severity says how bad a finding is (plan 5.1).
type Severity int

const (
	Warning Severity = iota + 1 // accepted, but probably not what was meant
	Error                       // the consumer rejects the file
	Blocker                     // the machine may not boot, or access may be lost
)

func (s Severity) String() string {
	switch s {
	case Warning:
		return "warning"
	case Error:
		return "error"
	case Blocker:
		return "blocker"
	}
	return "unknown"
}

// Finding is one problem in one file.
type Finding struct {
	Rule     string   // a rule id from rules.go, e.g. "fstab-source-missing"
	Severity Severity // the rule's, unless the checker has reason to change it
	Path     string   // the real path, never the scratch copy
	Line     int      // 0 when unknown
	Text     string   // one sentence of ours; never a secret
	Raw      string   // the validator's own lines; never logged by scd
}
