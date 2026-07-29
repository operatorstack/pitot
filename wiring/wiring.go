// Package wiring owns the host hook entries Pitot writes into repo-level
// coding-agent configs. Ownership is structural, ported from Boatstack's
// proven machinery:
//
//   - wiring-is-marker-owned-and-witnessed — Pitot edits only entries whose
//     command carries the marker (".pitot/bin/"); the committed fragment
//     .pitot/hooks/<host>.fragment.json witnesses exactly what was installed.
//   - foreign-config-is-never-repaired — malformed host config is a hard
//     error, never overwritten; repairs rewrite marked entries only.
//   - one-canonical-entry-per-event — merging strips every marked entry and
//     appends exactly one canonical entry, deduplicating collisions.
//   - wiring-points-at-the-repo-shim — every entry and bridge references
//     .pitot/bin/…, never a PATH binary, so the pin laws hold end to end.
//   - drift-is-named-not-silently-fixed — Check classifies each owned event
//     as FOUND, DRIFTED, or MISSING against the fragment witness; only an
//     explicit fix path mutates.
package wiring

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Marker identifies Pitot-owned hook entries: every command Pitot writes
// references the repo shim directory.
const Marker = ".pitot/bin/"

// FragmentDir holds the per-host ownership witnesses.
const FragmentDir = ".pitot/hooks"

// BridgeDir holds committed exit-code-to-JSON bridge scripts for hosts whose
// hook protocol cannot consume exit codes directly.
const BridgeDir = ".pitot/bin/hooks"

// repoHosts enumerates the hosts wireable at repository level, with the
// config file each owns and the blocking shell event Pitot supervises. Only
// the pre-tool shell boundary is wired: Pitot's protocol is pre-decision
// transport, and non-shell events would fail decode into spurious denials.
var repoHosts = map[string]struct {
	configPath string
	event      string
}{
	"claude": {configPath: ".claude/settings.json", event: "PreToolUse"},
	"codex":  {configPath: ".codex/hooks.json", event: "PreToolUse"},
	"cursor": {configPath: ".cursor/hooks.json", event: "beforeShellExecution"},
	"gemini": {configPath: ".gemini/settings.json", event: "BeforeTool"},
}

// Supported reports whether a host is wireable at repository level.
func Supported(host string) bool {
	_, ok := repoHosts[host]
	return ok
}

// RepoHosts lists the repo-level wireable hosts in sorted order.
func RepoHosts() []string {
	hosts := make([]string, 0, len(repoHosts))
	for host := range repoHosts {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

// ConfigPath returns the repo-relative host config file a host owns.
func ConfigPath(host string) string {
	return repoHosts[host].configPath
}

// Event returns the blocking hook event Pitot wires for a host.
func Event(host string) string {
	return repoHosts[host].event
}

// CanonicalEntry is the single hook entry Pitot installs for a host's
// blocking shell event. Commands reference the repo shim (or a committed
// bridge that execs it) — never a PATH binary.
func CanonicalEntry(host string) map[string]any {
	switch host {
	case "claude":
		return map[string]any{
			"matcher": "Bash",
			"hooks": []any{map[string]any{
				"type":    "command",
				"command": `"$CLAUDE_PROJECT_DIR"/.pitot/bin/pitot hook claude`,
				"timeout": float64(10),
			}},
		}
	case "codex":
		return map[string]any{
			"matcher": "Bash",
			"hooks": []any{map[string]any{
				"type":           "command",
				"command":        `.pitot/bin/pitot hook codex`,
				"commandWindows": `powershell -NoProfile -ExecutionPolicy Bypass -File .pitot/bin/pitot.ps1 hook codex`,
				"timeout":        float64(10),
			}},
		}
	case "cursor":
		return map[string]any{
			"command":        `./.pitot/bin/hooks/cursor-beforeShellExecution`,
			"commandWindows": `powershell -NoProfile -ExecutionPolicy Bypass -File .pitot/bin/pitot.ps1 hook cursor`,
			"failClosed":     true,
			"timeout":        float64(10),
		}
	case "gemini":
		return map[string]any{
			"matcher":    "run_shell_command",
			"sequential": true,
			"hooks": []any{map[string]any{
				"name":    "pitot",
				"type":    "command",
				"command": `./.pitot/bin/hooks/gemini-BeforeTool`,
				"timeout": float64(10000),
			}},
		}
	}
	return nil
}

// containsMarker walks any decoded JSON value looking for the ownership
// marker in string leaves.
func containsMarker(value any) bool {
	switch typed := value.(type) {
	case string:
		return bytes.Contains([]byte(typed), []byte(Marker))
	case []any:
		for _, item := range typed {
			if containsMarker(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if containsMarker(item) {
				return true
			}
		}
	}
	return false
}

// Merge installs the canonical entry for a host into a decoded config:
// every marked entry is stripped (deduplicating collisions), every foreign
// entry is preserved in order, and exactly one canonical entry is appended.
// Malformed structure is a hard error — foreign config is never repaired.
func Merge(config map[string]any, host string) error {
	if !Supported(host) {
		return fmt.Errorf("pitot: host %q is not wireable at repository level", host)
	}
	hooksValue, exists := config["hooks"]
	if !exists || hooksValue == nil {
		hooksValue = map[string]any{}
		config["hooks"] = hooksValue
	}
	hooks, ok := hooksValue.(map[string]any)
	if !ok {
		return fmt.Errorf("pitot: %s has a non-object hooks section; refusing to touch foreign configuration (foreign-config-is-never-repaired)", ConfigPath(host))
	}

	// A marked entry on an event Pitot does not own for this host is drift
	// from an older layout — refuse rather than guess.
	for event, value := range hooks {
		if event == Event(host) {
			continue
		}
		if containsMarker(value) {
			return fmt.Errorf("pitot: %s carries a Pitot-owned entry on unowned event %q; remove it or re-run with the current layout (drift-is-named-not-silently-fixed)", ConfigPath(host), event)
		}
	}

	event := Event(host)
	existing, exists := hooks[event]
	var entries []any
	if exists && existing != nil {
		list, ok := existing.([]any)
		if !ok {
			return fmt.Errorf("pitot: %s event %q is not a list; refusing to touch foreign configuration (foreign-config-is-never-repaired)", ConfigPath(host), event)
		}
		entries = list
	}
	kept := make([]any, 0, len(entries)+1)
	for _, entry := range entries {
		if containsMarker(entry) {
			continue // stripped; the canonical entry replaces every marked one
		}
		kept = append(kept, entry)
	}
	kept = append(kept, CanonicalEntry(host))
	hooks[event] = kept

	if host == "cursor" {
		if _, exists := config["version"]; !exists {
			config["version"] = float64(1)
		}
	}
	return nil
}

// Fragment renders the ownership witness for a host.
func Fragment(host string) ([]byte, error) {
	payload := map[string]any{
		"schema_version": 1,
		"host":           host,
		"events": map[string]any{
			Event(host): CanonicalEntry(host),
		},
	}
	rendered, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(rendered, '\n'), nil
}

// FragmentPath is the repo-relative witness path for a host.
func FragmentPath(host string) string {
	return filepath.ToSlash(filepath.Join(FragmentDir, host+".fragment.json"))
}

// EventState classifies one owned event against the fragment witness.
type EventState string

const (
	StateFound   EventState = "FOUND"
	StateDrifted EventState = "DRIFTED"
	StateMissing EventState = "MISSING"
)

// Check compares the host config against the committed fragment witness.
// The distinction matters: template migration (new release, old fragment)
// is not user drift — drift means the config no longer matches what Pitot
// itself installed.
func Check(root, host string) (EventState, error) {
	if !Supported(host) {
		return "", fmt.Errorf("pitot: host %q is not wireable at repository level", host)
	}
	fragmentRaw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(FragmentPath(host))))
	if err != nil {
		return "", fmt.Errorf("pitot: %s is not wired (no fragment witness at %s); run 'pitot init --host %s'", host, FragmentPath(host), host)
	}
	var fragment struct {
		SchemaVersion int                       `json:"schema_version"`
		Host          string                    `json:"host"`
		Events        map[string]map[string]any `json:"events"`
	}
	if err := json.Unmarshal(fragmentRaw, &fragment); err != nil || fragment.SchemaVersion != 1 || fragment.Host != host || len(fragment.Events) == 0 {
		return "", fmt.Errorf("pitot: fragment %s is invalid; re-run 'pitot init --host %s --force'", FragmentPath(host), host)
	}

	configRaw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(ConfigPath(host))))
	if err != nil {
		return StateMissing, nil
	}
	var config map[string]any
	if err := json.Unmarshal(configRaw, &config); err != nil {
		return "", fmt.Errorf("pitot: %s is not valid JSON; refusing to interpret it (foreign-config-is-never-repaired)", ConfigPath(host))
	}
	hooks, _ := config["hooks"].(map[string]any)

	event := Event(host)
	witness := fragment.Events[event]
	entries, _ := hooks[event].([]any)
	marked := 0
	drifted := false
	for _, entry := range entries {
		if !containsMarker(entry) {
			continue
		}
		marked++
		if !sameJSON(entry, witness) {
			drifted = true
		}
	}
	// A marked entry anywhere else is drift too.
	for other, value := range hooks {
		if other != event && containsMarker(value) {
			drifted = true
		}
	}
	switch {
	case marked == 0:
		return StateMissing, nil
	case drifted || marked > 1:
		return StateDrifted, nil
	default:
		return StateFound, nil
	}
}

func sameJSON(a, b any) bool {
	left, errA := json.Marshal(a)
	right, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(left, right)
}

// Bridges returns the committed bridge scripts a host needs (repo-relative
// path -> content). The bridges translate Pitot's exit-code protocol into
// the host's native decision JSON and exec the sibling repo shim by default
// (wiring-points-at-the-repo-shim); PITOT_BIN remains a test override.
func Bridges(host string) map[string]string {
	switch host {
	case "cursor":
		return map[string]string{
			BridgeDir + "/cursor-beforeShellExecution": `#!/bin/sh
# Pitot cursor bridge — generated by 'pitot init --host cursor'. Translates
# the shim's exit-code protocol into Cursor's decision JSON. Carries no
# policy: the Controller behind the runtime decides.
set -u

shim="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)/pitot"
PITOT_COMMAND="${PITOT_BIN:-$shim}"
PAYLOAD=$(cat)
if PITOT_ERROR=$(printf '%s' "$PAYLOAD" | "$PITOT_COMMAND" hook cursor 2>&1 >/dev/null); then
  printf '%s\n' '{"continue":true,"permission":"allow"}'
  exit 0
fi
python3 -c 'import json,sys; reason=(sys.argv[1] or "Pitot rejected the shell request")[:1024]; print(json.dumps({"continue":True,"permission":"deny","user_message":reason,"agent_message":reason},separators=(",",":")))' "$PITOT_ERROR"
exit 0
`,
		}
	case "gemini":
		return map[string]string{
			BridgeDir + "/gemini-BeforeTool": `#!/bin/sh
# Pitot gemini bridge — generated by 'pitot init --host gemini'. Translates
# the shim's exit-code protocol into Gemini's decision JSON. Carries no
# policy: the Controller behind the runtime decides.
set -u

shim="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)/pitot"
PITOT_COMMAND="${PITOT_BIN:-$shim}"
PAYLOAD=$(cat)
if PITOT_ERROR=$(printf '%s' "$PAYLOAD" | "$PITOT_COMMAND" hook gemini 2>&1 >/dev/null); then
  printf '%s\n' '{"decision":"allow"}'
  exit 0
fi
python3 -c 'import json,sys; print(json.dumps({"decision":"deny","reason":(sys.argv[1] or "Pitot rejected the shell request")[:1024]},separators=(",",":")))' "$PITOT_ERROR"
exit 0
`,
		}
	}
	return nil
}
