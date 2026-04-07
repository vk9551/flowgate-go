package engine

import (
	"testing"

	"github.com/vk9551/flowgate-io/internal/config"
)

func boolPtr(b bool) *bool { return &b }

// basePriorities is a representative config used across multiple test cases.
var basePriorities = []config.Priority{
	{
		Name: "critical",
		Match: []config.MatchRule{
			{Field: "type", In: []string{"otp", "order_confirmed"}},
		},
		BypassAll: true,
	},
	{
		Name: "transactional",
		Match: []config.MatchRule{
			{Field: "type", Prefix: "txn_"},
		},
	},
	{
		Name: "bulk",
		Match: []config.MatchRule{
			{Field: "type", Prefix: "marketing_"},
		},
		Default: true,
	},
}

func TestMatchPriority(t *testing.T) {
	tests := []struct {
		name       string
		priorities []config.Priority
		event      Event
		wantName   string // "" means nil expected
	}{
		// --- In matcher ---
		{
			name:       "in: exact match first value",
			priorities: basePriorities,
			event:      Event{"type": "otp"},
			wantName:   "critical",
		},
		{
			name:       "in: exact match second value",
			priorities: basePriorities,
			event:      Event{"type": "order_confirmed"},
			wantName:   "critical",
		},
		{
			name:       "in: no match falls to default",
			priorities: basePriorities,
			event:      Event{"type": "newsletter"},
			wantName:   "bulk",
		},

		// --- Prefix matcher ---
		{
			name:       "prefix: matches transactional",
			priorities: basePriorities,
			event:      Event{"type": "txn_refund"},
			wantName:   "transactional",
		},
		{
			name:       "prefix: marketing_ matches default bulk",
			priorities: basePriorities,
			event:      Event{"type": "marketing_weekly"},
			wantName:   "bulk",
		},
		{
			name:       "prefix: partial prefix does not match",
			priorities: basePriorities,
			event:      Event{"type": "txn"},
			wantName:   "bulk", // falls to default
		},

		// --- Suffix matcher ---
		{
			name: "suffix: matches",
			priorities: []config.Priority{
				{Name: "digest", Match: []config.MatchRule{{Field: "type", Suffix: "_digest"}}},
				{Name: "fallback", Default: true},
			},
			event:    Event{"type": "weekly_digest"},
			wantName: "digest",
		},
		{
			name: "suffix: no match falls to default",
			priorities: []config.Priority{
				{Name: "digest", Match: []config.MatchRule{{Field: "type", Suffix: "_digest"}}},
				{Name: "fallback", Default: true},
			},
			event:    Event{"type": "weekly_summary"},
			wantName: "fallback",
		},

		// --- Equals matcher ---
		{
			name: "equals: exact match",
			priorities: []config.Priority{
				{Name: "ping", Match: []config.MatchRule{{Field: "type", Equals: "ping"}}},
			},
			event:    Event{"type": "ping"},
			wantName: "ping",
		},
		{
			name: "equals: case sensitive no match",
			priorities: []config.Priority{
				{Name: "ping", Match: []config.MatchRule{{Field: "type", Equals: "ping"}}},
			},
			event:    Event{"type": "Ping"},
			wantName: "",
		},

		// --- Exists matcher ---
		{
			name: "exists true: field present",
			priorities: []config.Priority{
				{Name: "tagged", Match: []config.MatchRule{{Field: "tag", Exists: boolPtr(true)}}},
			},
			event:    Event{"tag": "vip"},
			wantName: "tagged",
		},
		{
			name: "exists true: field absent",
			priorities: []config.Priority{
				{Name: "tagged", Match: []config.MatchRule{{Field: "tag", Exists: boolPtr(true)}}},
			},
			event:    Event{"type": "otp"},
			wantName: "",
		},
		{
			name: "exists false: field absent",
			priorities: []config.Priority{
				{Name: "untagged", Match: []config.MatchRule{{Field: "tag", Exists: boolPtr(false)}}},
			},
			event:    Event{"type": "otp"},
			wantName: "untagged",
		},
		{
			name: "exists false: field present",
			priorities: []config.Priority{
				{Name: "untagged", Match: []config.MatchRule{{Field: "tag", Exists: boolPtr(false)}}},
			},
			event:    Event{"tag": "vip"},
			wantName: "",
		},

		// --- Multi-rule AND logic ---
		{
			name: "multi-rule: all match",
			priorities: []config.Priority{
				{
					Name: "vip_otp",
					Match: []config.MatchRule{
						{Field: "type", Equals: "otp"},
						{Field: "tier", Equals: "vip"},
					},
				},
			},
			event:    Event{"type": "otp", "tier": "vip"},
			wantName: "vip_otp",
		},
		{
			name: "multi-rule: partial match fails",
			priorities: []config.Priority{
				{
					Name: "vip_otp",
					Match: []config.MatchRule{
						{Field: "type", Equals: "otp"},
						{Field: "tier", Equals: "vip"},
					},
				},
			},
			event:    Event{"type": "otp", "tier": "standard"},
			wantName: "",
		},

		// --- First-match wins ---
		{
			name: "first match wins over later match",
			priorities: []config.Priority{
				{Name: "first", Match: []config.MatchRule{{Field: "type", Equals: "otp"}}},
				{Name: "second", Match: []config.MatchRule{{Field: "type", Equals: "otp"}}},
			},
			event:    Event{"type": "otp"},
			wantName: "first",
		},

		// --- Default behaviour ---
		{
			name:       "no match, no default returns nil",
			priorities: []config.Priority{
				{Name: "critical", Match: []config.MatchRule{{Field: "type", Equals: "otp"}}},
			},
			event:    Event{"type": "newsletter"},
			wantName: "",
		},
		{
			name: "empty rules priority never matches explicitly",
			priorities: []config.Priority{
				{Name: "empty", Match: []config.MatchRule{}},
				{Name: "fallback", Default: true},
			},
			event:    Event{"type": "anything"},
			wantName: "fallback",
		},

		// --- Missing field ---
		{
			name:       "missing field in event — no match",
			priorities: basePriorities,
			event:      Event{"channel": "email"}, // no "type" field
			wantName:   "bulk",                    // falls to default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchPriority(tt.priorities, tt.event)
			if tt.wantName == "" {
				if got != nil {
					t.Errorf("expected nil, got priority %q", got.Name)
				}
				return
			}
			if got == nil {
				t.Errorf("expected priority %q, got nil", tt.wantName)
				return
			}
			if got.Name != tt.wantName {
				t.Errorf("expected %q, got %q", tt.wantName, got.Name)
			}
		})
	}
}
