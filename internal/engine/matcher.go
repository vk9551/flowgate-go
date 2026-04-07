package engine

import (
	"strings"

	"github.com/vk9551/flowgate-io/internal/config"
)

// Event is an arbitrary key-value payload submitted by the caller.
type Event map[string]string

// MatchPriority returns the first priority whose match rules are satisfied
// by the event. If no priority matches, the default priority is returned.
// If there is no default, nil is returned.
func MatchPriority(priorities []config.Priority, event Event) *config.Priority {
	var defaultPriority *config.Priority

	for i := range priorities {
		p := &priorities[i]
		if p.Default {
			defaultPriority = p
			continue // keep scanning; an explicit match beats default
		}
		if matchesAll(p.Match, event) {
			return p
		}
	}

	// No explicit match — fall back to default if one exists.
	return defaultPriority
}

// matchesAll returns true when every rule in rules is satisfied by event.
// An empty rules slice matches nothing (a priority with no rules is inert
// unless it is marked default).
func matchesAll(rules []config.MatchRule, event Event) bool {
	if len(rules) == 0 {
		return false
	}
	for _, r := range rules {
		if !matchesRule(r, event) {
			return false
		}
	}
	return true
}

// matchesRule evaluates a single MatchRule against the event.
func matchesRule(r config.MatchRule, event Event) bool {
	val, present := event[r.Field]

	// exists check — must happen before string comparisons
	if r.Exists != nil {
		return present == *r.Exists
	}

	if !present {
		return false
	}

	if len(r.In) > 0 {
		for _, v := range r.In {
			if val == v {
				return true
			}
		}
		return false
	}

	if r.Prefix != "" {
		return strings.HasPrefix(val, r.Prefix)
	}

	if r.Suffix != "" {
		return strings.HasSuffix(val, r.Suffix)
	}

	if r.Equals != "" {
		return val == r.Equals
	}

	// No matcher specified — treat as a field-presence check.
	return present
}
