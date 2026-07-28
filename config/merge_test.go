package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTenant registers a fragment under root/.pitot/conf.d.
func writeTenant(t *testing.T, root, name, raw string) {
	t.Helper()
	dir := filepath.Join(root, ".pitot", "conf.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

const tenantController = `controllers:
  shell:
    id: shell-policy
    command: ["policy"]
    dir: tool-a
    deadline_ms: 1000
    on_timeout: deny
    on_unavailable: deny
`

const tenantExplicit = `requires_protocol: "1"
controllers:
  interlock.effect:
    id: interlock
    command: ["interlock-controller"]
    deadline_ms: 1000
    on_timeout: deny
    on_unavailable: deny
`

const tenantConsumer = `consumers:
  - id: audit
    command: ["audit-log"]
    events: ["action.requested"]
    projection: {content: sha256}
`

func TestDiscoverMergesTenantFragments(t *testing.T) {
	root := t.TempDir()
	writeTenant(t, root, "boatstack.yaml", tenantController)
	writeTenant(t, root, "interlock.yaml", tenantExplicit)
	writeTenant(t, root, "meter.yml", tenantConsumer)

	loaded, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Config.Controllers) != 2 {
		t.Fatalf("merged controllers = %+v, want shell + interlock.effect", loaded.Config.Controllers)
	}
	if loaded.Config.Controllers["shell"].Dir != "tool-a" {
		t.Errorf("shell controller dir = %q, want tool-a", loaded.Config.Controllers["shell"].Dir)
	}
	if len(loaded.Config.Consumers) != 1 || loaded.Config.Consumers[0].ID != "audit" {
		t.Errorf("merged consumers = %+v, want the audit consumer", loaded.Config.Consumers)
	}
	if len(loaded.SHA256) != 64 {
		t.Errorf("merged digest %q is not 64 hex chars", loaded.SHA256)
	}
}

// The merged digest must be a pure function of the sorted fragment set: stable
// across repeated discovery, changed by any content change.
func TestDiscoverDigestIsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeTenant(t, root, "a.yaml", tenantController)
	writeTenant(t, root, "b.yaml", tenantConsumer)

	first, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 != second.SHA256 {
		t.Fatalf("digest not stable: %s vs %s", first.SHA256, second.SHA256)
	}
	writeTenant(t, root, "b.yaml", strings.Replace(tenantConsumer, "audit", "audit2", 1))
	third, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if third.SHA256 == first.SHA256 {
		t.Fatal("digest did not change with fragment content")
	}
}

func TestDiscoverNoFragments(t *testing.T) {
	if _, err := Discover(t.TempDir()); !errors.Is(err, ErrNoConfig) {
		t.Fatalf("empty root: want ErrNoConfig, got %v", err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".pitot", "conf.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(root); !errors.Is(err, ErrNoConfig) {
		t.Fatalf("empty conf.d: want ErrNoConfig, got %v", err)
	}
}

// Collisions across tenants are hard errors that name both source files, so
// the loser knows exactly whose registration it ran into.
func TestDiscoverCollisionsNameBothFragments(t *testing.T) {
	cases := []struct {
		name    string
		second  string
		wantErr string
	}{
		{
			name:    "request kind claimed twice",
			second:  strings.Replace(tenantController, "shell-policy", "other-policy", 1),
			wantErr: `request kind "shell" is claimed by both .pitot/conf.d/a.yaml and .pitot/conf.d/z.yaml`,
		},
		{
			name:    "controller id declared twice",
			second:  strings.Replace(tenantController, "shell:", "other.kind:", 1),
			wantErr: `controller id "shell-policy" is declared by both .pitot/conf.d/a.yaml and .pitot/conf.d/z.yaml`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTenant(t, root, "a.yaml", tenantController)
			writeTenant(t, root, "z.yaml", tc.second)
			_, err := Discover(root)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}

	t.Run("consumer id declared twice", func(t *testing.T) {
		root := t.TempDir()
		writeTenant(t, root, "a.yaml", tenantConsumer)
		writeTenant(t, root, "z.yaml", tenantConsumer)
		_, err := Discover(root)
		want := `consumer id "audit" is declared by both .pitot/conf.d/a.yaml and .pitot/conf.d/z.yaml`
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("want error containing %q, got %v", want, err)
		}
	})
}

func TestDiscoverRejectsBrokenFragments(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{"empty fragment", "consumers: []\n", "declares no consumers or controllers"},
		{"unknown field", "controllers: {}\nowner: me\n", "field owner not found"},
		{"protocol mismatch", strings.Replace(tenantExplicit, `"1"`, `"9"`, 1), `requires protocol "9" but this pitot speaks protocol "1"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTenant(t, root, "bad.yaml", tc.raw)
			_, err := Discover(root)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
			if err != nil && !strings.Contains(err.Error(), "bad.yaml") && tc.name != "protocol mismatch" {
				t.Fatalf("error must name the offending fragment, got %v", err)
			}
		})
	}
}

func TestValidateRejectsAbsoluteDir(t *testing.T) {
	root := t.TempDir()
	abs := root // any absolute path
	writeTenant(t, root, "a.yaml", strings.Replace(tenantController, "dir: tool-a", "dir: "+abs, 1))
	_, err := Discover(root)
	if err == nil || !strings.Contains(err.Error(), "must be relative") {
		t.Fatalf("want relative-dir rejection, got %v", err)
	}
}
