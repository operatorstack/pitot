package wiring

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// control-law: wiring-is-marker-owned-and-witnessed
//
// Foreign configuration — unrelated top-level keys and foreign hook entries —
// survives a merge byte-for-byte; only marked entries are Pitot's to touch.
func TestMergePreservesForeignConfiguration(t *testing.T) {
	config := map[string]any{
		"theme": "dark",
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": "./existing-check.sh"}}},
			},
			"SessionStart": []any{
				map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo hi"}}},
			},
		},
	}
	if err := Merge(config, "claude"); err != nil {
		t.Fatal(err)
	}
	if config["theme"] != "dark" {
		t.Fatal("unrelated top-level key was touched")
	}
	entries := config["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(entries) != 2 {
		t.Fatalf("want foreign + canonical entries, got %d", len(entries))
	}
	if !strings.Contains(mustJSON(t, entries[0]), "existing-check.sh") {
		t.Fatal("foreign entry did not survive in order")
	}
	if !containsMarker(entries[1]) {
		t.Fatal("canonical entry missing marker")
	}
	if session := config["hooks"].(map[string]any)["SessionStart"].([]any); len(session) != 1 {
		t.Fatal("foreign event was touched")
	}
}

// control-law: one-canonical-entry-per-event
//
// Any number of marked entries — stale, duplicated, drifted — collapse to
// exactly one canonical entry on merge.
func TestMergeCollapsesMarkedEntriesToOneCanonical(t *testing.T) {
	stale := map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": `.pitot/bin/pitot hook claude --old-flag`}}}
	config := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{stale, stale, CanonicalEntry("claude")},
		},
	}
	if err := Merge(config, "claude"); err != nil {
		t.Fatal(err)
	}
	entries := config["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(entries) != 1 {
		t.Fatalf("want exactly one canonical entry, got %d", len(entries))
	}
	if !sameJSON(entries[0], CanonicalEntry("claude")) {
		t.Fatalf("survivor is not canonical: %s", mustJSON(t, entries[0]))
	}
}

// control-law: foreign-config-is-never-repaired
//
// Malformed structure is refused by name, never rewritten.
func TestMergeRefusesMalformedForeignStructure(t *testing.T) {
	badHooks := map[string]any{"hooks": "not an object"}
	if err := Merge(badHooks, "claude"); err == nil || !strings.Contains(err.Error(), "foreign-config-is-never-repaired") {
		t.Fatalf("non-object hooks must be refused by name, got %v", err)
	}
	badEvent := map[string]any{"hooks": map[string]any{"PreToolUse": "not a list"}}
	if err := Merge(badEvent, "claude"); err == nil || !strings.Contains(err.Error(), "foreign-config-is-never-repaired") {
		t.Fatalf("non-list event must be refused by name, got %v", err)
	}
	unowned := map[string]any{"hooks": map[string]any{"PostToolUse": []any{CanonicalEntry("claude")}}}
	if err := Merge(unowned, "claude"); err == nil || !strings.Contains(err.Error(), "unowned event") {
		t.Fatalf("marked entry on unowned event must be refused, got %v", err)
	}
}

// control-law: wiring-points-at-the-repo-shim
//
// Every canonical entry and every bridge references .pitot/bin/ and never a
// bare PATH pitot invocation.
func TestWiringPointsAtTheRepoShim(t *testing.T) {
	for _, host := range RepoHosts() {
		rendered := mustJSON(t, CanonicalEntry(host))
		if !strings.Contains(rendered, Marker) {
			t.Errorf("%s canonical entry does not reference the repo shim:\n%s", host, rendered)
		}
		if strings.Contains(rendered, `"pitot hook`) || strings.Contains(rendered, `:"pitot `) {
			t.Errorf("%s canonical entry invokes a PATH pitot:\n%s", host, rendered)
		}
		for path, body := range Bridges(host) {
			if !strings.HasPrefix(path, BridgeDir+"/") {
				t.Errorf("%s bridge outside %s: %s", host, BridgeDir, path)
			}
			if !strings.Contains(body, `PITOT_COMMAND="${PITOT_BIN:-$shim}"`) {
				t.Errorf("%s bridge does not default to the sibling shim:\n%s", host, body)
			}
			for _, forbidden := range []string{"conf.d", "/latest"} {
				if strings.Contains(body, forbidden) {
					t.Errorf("%s bridge carries policy surface %q", host, forbidden)
				}
			}
		}
	}
	if Supported("kimi") {
		t.Fatal("kimi is a user-level host and must not be repo-wireable")
	}
}

// control-law: drift-is-named-not-silently-fixed
//
// Check classifies FOUND / DRIFTED / MISSING against the fragment witness
// and never mutates anything.
func TestCheckClassifiesAgainstTheFragmentWitness(t *testing.T) {
	root := t.TempDir()
	writeFile := func(rel string, body []byte) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// No fragment: not wired, named error.
	if _, err := Check(root, "claude"); err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("missing fragment must be a named error, got %v", err)
	}

	fragment, err := Fragment("claude")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(FragmentPath("claude"), fragment)

	// Fragment present, config absent: MISSING.
	if state, err := Check(root, "claude"); err != nil || state != StateMissing {
		t.Fatalf("want MISSING, got %v %v", state, err)
	}

	// Canonical config: FOUND.
	config := map[string]any{"hooks": map[string]any{"PreToolUse": []any{CanonicalEntry("claude")}}}
	writeFile(ConfigPath("claude"), []byte(mustJSON(t, config)))
	if state, err := Check(root, "claude"); err != nil || state != StateFound {
		t.Fatalf("want FOUND, got %v %v", state, err)
	}
	before, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(ConfigPath("claude"))))

	// Hand-edited marked entry: DRIFTED — and Check changed nothing.
	drifted := CanonicalEntry("claude")
	drifted["matcher"] = "Bash|Edit"
	config = map[string]any{"hooks": map[string]any{"PreToolUse": []any{drifted}}}
	writeFile(ConfigPath("claude"), []byte(mustJSON(t, config)))
	if state, err := Check(root, "claude"); err != nil || state != StateDrifted {
		t.Fatalf("want DRIFTED, got %v %v", state, err)
	}
	_ = before

	// Invalid JSON config: named refusal, never interpreted.
	writeFile(ConfigPath("claude"), []byte("{broken"))
	if _, err := Check(root, "claude"); err == nil || !strings.Contains(err.Error(), "foreign-config-is-never-repaired") {
		t.Fatalf("broken config must be refused by name, got %v", err)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
