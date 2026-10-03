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

// Added returns the findings of after that before does not have, in
// after's order (plan 5.2): what an edit added. A stock system already
// fails some validators, so only these are blamed on the edit. A finding
// is the same when its rule, severity and text are; its line may have
// moved. The same finding twice in after and once in before is one added;
// a finding whose severity changed (nofail removed from a line whose disk
// is missing) is added.
func Added(before, after []Finding) []Finding {
	type key struct {
		rule string
		sev  Severity
		text string
	}
	had := map[key]int{}
	for _, f := range before {
		had[key{f.Rule, f.Severity, f.Text}]++
	}
	var out []Finding
	for _, f := range after {
		k := key{f.Rule, f.Severity, f.Text}
		if had[k] > 0 {
			had[k]--
			continue
		}
		out = append(out, f)
	}
	return out
}

// Worst returns the highest severity among fs, or 0 when there are none.
func Worst(fs []Finding) Severity {
	var w Severity
	for _, f := range fs {
		w = max(w, f.Severity)
	}
	return w
}
