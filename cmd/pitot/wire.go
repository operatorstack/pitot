package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/operatorstack/pitot/config"
	"github.com/operatorstack/pitot/wiring"
)

// runWireHost is `pitot init --host HOST`: ensure the repo substrate, emit
// any bridges the host needs, merge the marker-owned entry into the host
// config, and write the fragment witness. Idempotent; a DRIFTED entry is
// refused without force (drift-is-named-not-silently-fixed).
func runWireHost(host string, force bool, stdout io.Writer) error {
	if !wiring.Supported(host) {
		return printWiringGuidance(host, stdout)
	}
	root := "."
	if found, err := config.FindRoot("."); err == nil {
		root = found
	}

	if _, err := writeSubstrate(stdout, releaseVersion()); err != nil {
		return err
	}

	// Drift gate: if this host was wired before, a hand-edited entry blocks
	// an implicit rewrite.
	if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(wiring.FragmentPath(host)))); statErr == nil {
		state, err := wiring.Check(root, host)
		if err != nil {
			return err
		}
		if state == wiring.StateDrifted && !force {
			return fmt.Errorf("pitot init: the %s entry in %s drifted from the fragment witness %s; review it, then re-run with --force to restore the canonical entry (drift-is-named-not-silently-fixed)", host, wiring.ConfigPath(host), wiring.FragmentPath(host))
		}
	}

	written := []string{}
	for rel, body := range wiring.Bridges(host) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Stat(path); err == nil && !force {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			return err
		}
		written = append(written, rel)
	}

	configPath := filepath.Join(root, filepath.FromSlash(wiring.ConfigPath(host)))
	hostConfig := map[string]any{}
	if raw, err := os.ReadFile(configPath); err == nil {
		if err := json.Unmarshal(raw, &hostConfig); err != nil {
			return fmt.Errorf("pitot init: %s is not valid JSON; refusing to interpret it (foreign-config-is-never-repaired)", wiring.ConfigPath(host))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := wiring.Merge(hostConfig, host); err != nil {
		return err
	}
	rendered, err := json.MarshalIndent(hostConfig, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(configPath, append(rendered, '\n'), 0o644); err != nil {
		return err
	}
	written = append(written, wiring.ConfigPath(host))

	fragment, err := wiring.Fragment(host)
	if err != nil {
		return err
	}
	fragmentPath := filepath.Join(root, filepath.FromSlash(wiring.FragmentPath(host)))
	if err := os.MkdirAll(filepath.Dir(fragmentPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(fragmentPath, fragment, 0o644); err != nil {
		return err
	}
	written = append(written, wiring.FragmentPath(host))

	fmt.Fprintf(stdout, "Wired %s: one Pitot-owned %s entry in %s (foreign entries untouched)\n", host, wiring.Event(host), wiring.ConfigPath(host))
	fmt.Fprintf(stdout, "Files written: %s\n", joinSorted(written))
	fmt.Fprintf(stdout, "Verify any time: pitot doctor --host %s\n", host)
	return nil
}

// printWiringGuidance covers the hosts whose configuration lives outside the
// repository (user-level files or in-process plugins): Pitot prints the exact
// documented wiring instead of editing $HOME.
func printWiringGuidance(host string, stdout io.Writer) error {
	guidance := map[string]string{
		"devin":    "no hook configuration is required. Run:\n\n  pitot dev --host devin -- devin -p \"<prompt>\"\n\nFor a separately managed runtime, use:\n\n  pitot acp devin --runtime \"$PITOT_RUNTIME\" --prompt \"<prompt>\"",
		"kimi":     "add to ~/.kimi-code/config.toml (or $KIMI_CODE_HOME/config.toml):\n\n  [[hooks]]\n  event = \"PreToolUse\"\n  matcher = \"Bash\"\n  command = \"pitot hook kimi\"",
		"qwen":     "add to ~/.qwen/settings.json under hooks.PreToolUse:\n\n  {\"matcher\": \"^Bash$\", \"hooks\": [{\"type\": \"command\", \"command\": \"<path to integrations/qwen/PreToolUse>\"}]}",
		"copilot":  "add to ~/.copilot/settings.json under hooks.PreToolUse:\n\n  {\"matcher\": \"Bash\", \"hooks\": [{\"type\": \"command\", \"command\": \"<path to integrations/copilot/PreToolUse>\"}]}",
		"opencode": "register the plugin in ~/.config/opencode/opencode.json:\n\n  {\"plugin\": [\"file:///<path to integrations/opencode/pitot.ts>\"]}",
		"pi":       "copy integrations/pi/pitot.ts into ~/.pi/agent/extensions/ (or the repo's .pi/extensions/)",
	}
	text, known := guidance[host]
	if !known {
		return fmt.Errorf("pitot init: unsupported host %q (want one of: %s, or a guidance-only host: devin, kimi, qwen, copilot, opencode, pi)", host, joinSorted(wiring.RepoHosts()))
	}
	if host == "devin" {
		fmt.Fprintln(stdout, "devin uses a stateful ACP boundary; Pitot does not create lifecycle-hook configuration.")
	} else {
		fmt.Fprintf(stdout, "%s is configured at user level; Pitot does not edit files outside the repository.\n", host)
	}
	fmt.Fprintf(stdout, "To wire it, %s\n\nThen verify: pitot doctor --host %s\n", text, host)
	return nil
}

// printWiringStatus renders the doctor's view of a repo-wireable host.
func printWiringStatus(root, host string, stdout io.Writer) error {
	state, err := wiring.Check(root, host)
	if err != nil {
		fmt.Fprintf(stdout, "  wiring: %v\n", err)
		return err
	}
	switch state {
	case wiring.StateFound:
		fmt.Fprintf(stdout, "  wiring: FOUND — %s carries the canonical Pitot entry for %s\n", wiring.ConfigPath(host), wiring.Event(host))
		return nil
	case wiring.StateMissing:
		fmt.Fprintf(stdout, "  wiring: MISSING — no Pitot entry in %s; run 'pitot init --host %s' (or doctor --fix)\n", wiring.ConfigPath(host), host)
		return fmt.Errorf("pitot doctor: %s wiring is missing", host)
	default:
		fmt.Fprintf(stdout, "  wiring: DRIFTED — the Pitot entry in %s no longer matches the fragment witness %s; run 'pitot doctor --host %s --fix' to restore it\n", wiring.ConfigPath(host), wiring.FragmentPath(host), host)
		return fmt.Errorf("pitot doctor: %s wiring drifted", host)
	}
}

// anchorDirs resolves tenant working directories against the discovered
// root so `pitot run`/`dev` behave identically from any subdirectory.
func anchorDirs(cfg *config.Config, root string) {
	for kind, controller := range cfg.Controllers {
		controller.Dir = anchorDir(controller.Dir, root)
		cfg.Controllers[kind] = controller
	}
	for i := range cfg.Consumers {
		cfg.Consumers[i].Dir = anchorDir(cfg.Consumers[i].Dir, root)
	}
}

func anchorDir(dir, root string) string {
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(root, dir)
}

func joinSorted(values []string) string {
	out := ""
	for i, v := range sorted(values) {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}
