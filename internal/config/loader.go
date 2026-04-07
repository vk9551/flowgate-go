package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var envVarRe = regexp.MustCompile(`\$\{([^}]+)\}`)

// Load reads, env-expands, and validates a config file at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	expanded := expandEnv(string(raw))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	if err := parseDurations(&cfg); err != nil {
		return nil, err
	}

	if err := validate(&cfg); err != nil {
		return nil, err
	}

	applyDefaultOutcomes(&cfg)

	return &cfg, nil
}

// expandEnv replaces ${VAR} references with environment variable values.
func expandEnv(s string) string {
	return envVarRe.ReplaceAllStringFunc(s, func(match string) string {
		name := match[2 : len(match)-1]
		if val, ok := os.LookupEnv(name); ok {
			return val
		}
		return match // leave unexpanded if env var is absent
	})
}

// parseDurations converts raw duration strings (e.g. "1d", "48h") to time.Duration.
// Go's time.ParseDuration doesn't support "d", so we handle it ourselves.
func parseDurations(cfg *Config) error {
	for i := range cfg.Policies {
		p := &cfg.Policies[i]
		if p.Window.MaxDelayRaw != "" {
			d, err := parseDuration(p.Window.MaxDelayRaw)
			if err != nil {
				return fmt.Errorf("config: policy %q window.max_delay: %w", p.Priority, err)
			}
			p.Window.MaxDelay = d
		}
		for j := range p.Caps {
			c := &p.Caps[j]
			if c.PeriodRaw != "" {
				d, err := parseDuration(c.PeriodRaw)
				if err != nil {
					return fmt.Errorf("config: policy %q cap period: %w", p.Priority, err)
				}
				c.Period = d
			}
		}
		if p.Digest.WaitRaw != "" {
			d, err := parseDuration(p.Digest.WaitRaw)
			if err != nil {
				return fmt.Errorf("config: policy %q digest.wait: %w", p.Priority, err)
			}
			p.Digest.Wait = d
		}
	}
	return nil
}

// parseDuration extends time.ParseDuration with "d" (day) support.
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		days := strings.TrimSuffix(s, "d")
		var n int
		if _, err := fmt.Sscanf(days, "%d", &n); err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// applyDefaultOutcomes fills in built-in outcomes when none are configured in YAML.
func applyDefaultOutcomes(cfg *Config) {
	if len(cfg.Outcomes) == 0 {
		cfg.Outcomes = []OutcomeCfg{
			{Name: OutcomeNameSuccess, RefundCap: false, Terminal: true},
			{Name: OutcomeNameFailedTemp, RefundCap: true, Terminal: false},
			{Name: OutcomeNameFailedPerm, RefundCap: true, Terminal: true},
			{Name: OutcomeNamePending, RefundCap: false, Terminal: false},
		}
	}
	if cfg.DefaultOutcome == "" {
		cfg.DefaultOutcome = OutcomeNamePending
	}
}

// validate checks for required fields and logical consistency.
func validate(cfg *Config) error {
	if cfg.Subject.IDField == "" {
		return fmt.Errorf("config: subject.id_field is required")
	}

	priorityNames := make(map[string]bool)
	defaultCount := 0
	for _, p := range cfg.Priorities {
		if p.Name == "" {
			return fmt.Errorf("config: priority entry missing name")
		}
		if priorityNames[p.Name] {
			return fmt.Errorf("config: duplicate priority name %q", p.Name)
		}
		priorityNames[p.Name] = true
		if p.Default {
			defaultCount++
		}
	}
	if defaultCount > 1 {
		return fmt.Errorf("config: at most one priority may be marked default, found %d", defaultCount)
	}

	// Validate outcome names are unique.
	outcomeNames := make(map[string]bool)
	for _, o := range cfg.Outcomes {
		if o.Name == "" {
			return fmt.Errorf("config: outcome entry missing name")
		}
		if outcomeNames[o.Name] {
			return fmt.Errorf("config: duplicate outcome name %q", o.Name)
		}
		outcomeNames[o.Name] = true
	}
	if cfg.DefaultOutcome != "" && len(outcomeNames) > 0 && !outcomeNames[cfg.DefaultOutcome] {
		return fmt.Errorf("config: default_outcome %q not in outcomes list", cfg.DefaultOutcome)
	}

	for _, pol := range cfg.Policies {
		if pol.Priority == "" {
			return fmt.Errorf("config: policy entry missing priority name")
		}
		if !priorityNames[pol.Priority] {
			return fmt.Errorf("config: policy references unknown priority %q", pol.Priority)
		}
	}

	return nil
}
