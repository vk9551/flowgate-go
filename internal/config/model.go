package config

import "time"

// Config is the top-level structure for flowgate.yaml.
type Config struct {
	Version        string       `yaml:"version"`
	Subject        SubjectCfg   `yaml:"subject"`
	Priorities     []Priority   `yaml:"priorities"`
	Policies       []Policy     `yaml:"policies"`
	Callbacks      CallbacksCfg `yaml:"callbacks"`
	Storage        StorageCfg   `yaml:"storage"`
	Server         ServerCfg    `yaml:"server"`
	Outcomes       []OutcomeCfg `yaml:"outcomes"`        // delivery feedback outcomes; defaults applied if empty
	DefaultOutcome string       `yaml:"default_outcome"` // outcome assigned on event creation; default "pending"
}

// SubjectCfg describes how to identify and localize a subject.
type SubjectCfg struct {
	IDField       string      `yaml:"id_field"`
	TimezoneField string      `yaml:"timezone_field"`
	WakingHours   WakingHours `yaml:"waking_hours"`
}

// WakingHours defines the default active window (HH:MM format).
type WakingHours struct {
	Start string `yaml:"start"`
	End   string `yaml:"end"`
}

// Priority defines a named traffic class with match rules.
type Priority struct {
	Name      string      `yaml:"name"`
	Match     []MatchRule `yaml:"match"`
	BypassAll bool        `yaml:"bypass_all"`
	Default   bool        `yaml:"default"`
}

// MatchRule is a single field-level predicate.
// Exactly one of In, Prefix, Suffix, Equals, or Exists should be set.
type MatchRule struct {
	Field  string   `yaml:"field"`
	In     []string `yaml:"in"`
	Prefix string   `yaml:"prefix"`
	Suffix string   `yaml:"suffix"`
	Equals string   `yaml:"equals"`
	Exists *bool    `yaml:"exists"` // pointer so absent == unset
}

// Policy defines the decision rules for a priority tier.
type Policy struct {
	Priority            string    `yaml:"priority"`
	Decision            string    `yaml:"decision"` // send_now | suppress
	Window              WindowCfg `yaml:"window"`
	Caps                []CapRule `yaml:"caps"`
	DecisionOnCapBreach string    `yaml:"decision_on_cap_breach"`
	Digest              DigestCfg `yaml:"digest"`
}

// WindowCfg controls delay-window behaviour.
type WindowCfg struct {
	RespectWakingHours bool          `yaml:"respect_waking_hours"`
	MaxDelay           time.Duration `yaml:"-"` // parsed from MaxDelayRaw
	MaxDelayRaw        string        `yaml:"max_delay"`
}

// CapRule enforces a rate cap over a rolling period.
type CapRule struct {
	Scope     string        `yaml:"scope"` // subject | global
	Period    time.Duration `yaml:"-"`     // parsed from PeriodRaw
	PeriodRaw string        `yaml:"period"`
	Limit     int           `yaml:"limit"`
}

// DigestCfg enables digest mode: suppressed events are batched and delivered
// as a single digest callback after Wait duration (or when MaxItems is reached).
type DigestCfg struct {
	Enabled  bool          `yaml:"enabled"`
	Wait     time.Duration `yaml:"-"` // parsed from WaitRaw
	WaitRaw  string        `yaml:"wait"`
	MaxItems int           `yaml:"max_items"` // 0 means no item-count limit
}

// CallbacksCfg holds webhook targets for different decision outcomes.
type CallbacksCfg struct {
	SendNow     *CallbackTarget `yaml:"send_now"`
	Delayed     *CallbackTarget `yaml:"delayed"`
	Suppressed  *CallbackTarget `yaml:"suppressed"`
	DigestReady *CallbackTarget `yaml:"digest_ready"`
}

// CallbackTarget is a single webhook endpoint.
type CallbackTarget struct {
	URL           string `yaml:"url"`
	Method        string `yaml:"method"`
	IncludeReason bool   `yaml:"include_reason"`
	Retries       int    `yaml:"retries"`      // 0 → default (3)
	BackoffBase   string `yaml:"backoff_base"` // "" → default ("1s")
}

// StorageCfg selects and configures the storage backend.
type StorageCfg struct {
	Backend string `yaml:"backend"` // sqlite | redis | postgres
	DSN     string `yaml:"dsn"`
}

// ServerCfg holds HTTP server and auth settings.
type ServerCfg struct {
	Port      int     `yaml:"port"`
	Auth      AuthCfg `yaml:"auth"`
	Dashboard DashCfg `yaml:"dashboard"`
}

// AuthCfg configures authentication.
type AuthCfg struct {
	Type   string `yaml:"type"`   // jwt | api_key | none
	Secret string `yaml:"secret"` // supports ${ENV_VAR} expansion
}

// DashCfg toggles the embedded dashboard.
type DashCfg struct {
	Enabled bool `yaml:"enabled"`
}

// OutcomeCfg defines a named delivery outcome that callers can report back.
type OutcomeCfg struct {
	Name      string `yaml:"name"`
	RefundCap bool   `yaml:"refund_cap"` // true → remove event from cap window on this outcome
	Terminal  bool   `yaml:"terminal"`   // true → no further outcome updates allowed
}

// Default outcome names.
const (
	OutcomeNameSuccess    = "success"
	OutcomeNameFailedTemp = "failed_temp"
	OutcomeNameFailedPerm = "failed_perm"
	OutcomeNamePending    = "pending"
)
