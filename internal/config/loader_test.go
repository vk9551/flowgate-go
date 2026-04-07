package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "flowgate-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

func TestLoad_ValidMinimal(t *testing.T) {
	yaml := `
version: "1.0"
subject:
  id_field: user_id
priorities:
  - name: critical
    match:
      - field: type
        in: [otp]
    bypass_all: true
  - name: bulk
    match:
      - field: type
        prefix: marketing_
    default: true
policies:
  - priority: critical
    decision: act_now
  - priority: bulk
    decision: suppress
storage:
  backend: sqlite
server:
  port: 7700
`
	cfg, err := Load(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Subject.IDField != "user_id" {
		t.Errorf("id_field: got %q", cfg.Subject.IDField)
	}
	if len(cfg.Priorities) != 2 {
		t.Errorf("expected 2 priorities, got %d", len(cfg.Priorities))
	}
}

func TestLoad_DurationParsing(t *testing.T) {
	yaml := `
version: "1.0"
subject:
  id_field: user_id
priorities:
  - name: bulk
    default: true
policies:
  - priority: bulk
    window:
      max_delay: 48h
    caps:
      - scope: subject
        period: 1d
        limit: 5
`
	cfg, err := Load(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pol := cfg.Policies[0]
	if pol.Window.MaxDelay != 48*time.Hour {
		t.Errorf("max_delay: got %v", pol.Window.MaxDelay)
	}
	if pol.Caps[0].Period != 24*time.Hour {
		t.Errorf("cap period: got %v", pol.Caps[0].Period)
	}
}

func TestLoad_EnvExpansion(t *testing.T) {
	t.Setenv("TEST_SECRET", "mysecret")
	yaml := `
version: "1.0"
subject:
  id_field: user_id
server:
  auth:
    secret: "${TEST_SECRET}"
`
	cfg, err := Load(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.Auth.Secret != "mysecret" {
		t.Errorf("secret: got %q", cfg.Server.Auth.Secret)
	}
}

func TestLoad_MissingIDField(t *testing.T) {
	yaml := `
version: "1.0"
subject:
  id_field: ""
`
	_, err := Load(writeTemp(t, yaml))
	if err == nil {
		t.Fatal("expected error for missing id_field")
	}
}

func TestLoad_DuplicatePriorityName(t *testing.T) {
	yaml := `
version: "1.0"
subject:
  id_field: user_id
priorities:
  - name: bulk
  - name: bulk
`
	_, err := Load(writeTemp(t, yaml))
	if err == nil {
		t.Fatal("expected error for duplicate priority name")
	}
}

func TestLoad_PolicyReferencesUnknownPriority(t *testing.T) {
	yaml := `
version: "1.0"
subject:
  id_field: user_id
priorities:
  - name: critical
policies:
  - priority: ghost
    decision: act_now
`
	_, err := Load(writeTemp(t, yaml))
	if err == nil {
		t.Fatal("expected error for unknown priority reference")
	}
}

func TestLoad_MultipleDefaults(t *testing.T) {
	yaml := `
version: "1.0"
subject:
  id_field: user_id
priorities:
  - name: a
    default: true
  - name: b
    default: true
`
	_, err := Load(writeTemp(t, yaml))
	if err == nil {
		t.Fatal("expected error for multiple default priorities")
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
