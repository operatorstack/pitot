package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadStrictCompleteConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".pitot.yaml")
	raw := `consumers:
  - id: audit
    command: ["audit"]
    events: ["action.requested"]
    projection:
      content: sha256
controllers:
  shell:
    id: policy
    command: ["policy", "--jsonl"]
    deadline_ms: 2000
    on_timeout: deny
    on_unavailable: deny
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SHA256 == "" || len(loaded.Config.Consumers) != 1 || len(loaded.Config.Controllers) != 1 {
		t.Fatalf("unexpected loaded config: %+v", loaded)
	}
}

func TestConfigRejectsUnknownAndAmbiguousRoles(t *testing.T) {
	tests := []struct{ name, raw, want string }{
		{"unknown", "mystery: true\n", "field mystery not found"},
		{"empty", "consumers: []\ncontrollers: {}\n", "requires at least one"},
		{"duplicate consumer", "consumers:\n- {id: audit, command: [x], events: [action.requested], projection: {content: omit}}\n- {id: audit, command: [x], events: [action.requested], projection: {content: omit}}\n", "duplicate consumer"},
		{"duplicate controller id", "controllers:\n  shell: {id: policy, command: [x], deadline_ms: 1, on_timeout: deny, on_unavailable: deny}\n  release: {id: policy, command: [x], deadline_ms: 1, on_timeout: deny, on_unavailable: deny}\n", "registered for both"},
		{"unsupported event", "consumers:\n- {id: audit, command: [x], events: [model.usage], projection: {content: omit}}\n", "unsupported event"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".pitot.yaml")
			if err := os.WriteFile(path, []byte(test.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateRuntimeAllowsResolvedDirectories(t *testing.T) {
	cfg := Config{
		Consumers:   []ConsumerConfig{{ID: "audit", Command: []string{"consumer"}, Dir: t.TempDir(), Events: []string{"action.requested"}, Projection: ProjectionConfig{Content: "omit"}}},
		Controllers: map[string]ControllerConfig{"custom.approval": {ID: "policy", Command: []string{"controller"}, Dir: t.TempDir(), DeadlineMS: 1000, OnTimeout: "deny", OnUnavailable: "allow"}},
	}
	if err := cfg.ValidateRuntime(); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("file configuration accepted absolute working directories")
	}
}
