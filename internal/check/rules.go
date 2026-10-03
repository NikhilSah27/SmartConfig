package check

// Rule is one kind of finding with its fixed explanation (plan 5.4): what
// it means, why it matters and how to fix it, in two to five lines.
type Rule struct {
	ID       string
	Severity Severity
	Explain  string
}

// rules is every rule, in the order sc check -v lists them. Each checker
// adds its own with the step that builds it.
var rules = []Rule{}

// Lookup returns the rule with this id.
func Lookup(id string) (Rule, bool) {
	for _, r := range rules {
		if r.ID == id {
			return r, true
		}
	}
	return Rule{}, false
}
