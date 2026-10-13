package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/operatorstack/pitot/config"
	"github.com/operatorstack/pitot/hydrate"
	"github.com/operatorstack/pitot/wiring"
	"github.com/pelletier/go-toml/v2"
)

var doctorCodexSystemDirectory = codexSystemDirectory

func (r *readinessReport) codexSettings(path string) map[string]any {
	data, err := readDoctorFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	var settings map[string]any
	if err != nil || toml.Unmarshal(data, &settings) != nil {
		r.result("Codex settings", "INVALID", "a TOML configuration file is unreadable or malformed", true)
		return nil
	}
	r.result("Codex settings", "READ", path, false)
	return settings
}

func doctorCodex(stdout io.Writer) error {
	r := &readinessReport{out: stdout}
	r.binary("codex")
	root := "."
	if found, err := config.FindRoot("."); err == nil {
		root = found
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if err := printWiringStatus(root, "codex", stdout); err != nil {
		r.issues++
	}
	shim, expected := filepath.Join(root, shimDir, "pitot"), shimPOSIX
	if runtime.GOOS == "windows" {
		shim, expected = filepath.Join(root, shimDir, "pitot.ps1"), shimPowerShell
		r.binary("powershell")
	}
	if r.file("repository shim", shim, runtime.GOOS != "windows") {
		data, err := readDoctorFile(shim)
		if err != nil || !bytes.Equal(data, []byte(expected)) {
			r.result("repository shim", "UNVERIFIED", "shim differs from the generated script; review before restoring with init", true)
		}
	}
	if _, err := hydrate.Pin(root); err != nil {
		r.result("repository pin", "INVALID", "a valid .pitot/version is required", true)
	} else {
		r.result("repository pin", "FOUND", "release availability and hydration are not checked", false)
	}
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			r.result("Codex home", "UNVERIFIED", "home directory cannot be resolved", true)
			return r.finish()
		}
		home = filepath.Join(userHome, ".codex")
	}
	systemDir := doctorCodexSystemDirectory()
	system := r.codexSettings(filepath.Join(systemDir, "config.toml"))
	user := r.codexSettings(filepath.Join(home, "config.toml"))
	project := r.codexSettings(filepath.Join(root, ".codex", "config.toml"))
	policy := r.codexSettings(filepath.Join(systemDir, "requirements.toml"))
	if runtime.GOOS != "windows" {
		if legacy := r.codexSettings(filepath.Join(systemDir, "managed_config.toml")); len(legacy) != 0 {
			r.result("managed configuration", "UNVERIFIED", "legacy managed settings require native inspection", true)
		}
	}
	trusted := false
	projects, _ := user["projects"].(map[string]any)
	for path, raw := range projects {
		entry, _ := raw.(map[string]any)
		if sameDoctorPath(path, root) && entry["trust_level"] == "trusted" {
			trusted = true
		}
	}
	layers := []map[string]any{system, user}
	// Ancestor and invocation-directory layers can change the effective flags.
	// Do not silently treat the repository-root file as the entire tree.
	cwd, _ := os.Getwd()
	projectRoot := cwd
	for dir := cwd; dir != ""; dir = filepath.Dir(dir) {
		if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			valid := true
			if info.IsDir() {
				if _, err := os.Stat(filepath.Join(dir, ".git", "HEAD")); err != nil {
					valid = false
				}
			}
			if valid {
				projectRoot = dir
				break
			}
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	for _, layer := range []map[string]any{system, user} {
		if _, exists := layer["project_root_markers"]; exists {
			r.result("Codex project root", "UNVERIFIED", "custom project root markers require native discovery inspection", true)
		}
	}
	for dir := cwd; dir != ""; dir = filepath.Dir(dir) {
		if !sameDoctorPath(dir, root) && !sameDoctorPath(filepath.Join(dir, ".codex"), home) {
			path := filepath.Join(dir, ".codex", "config.toml")
			if _, err := os.Stat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
				r.result("Codex configuration layers", "UNVERIFIED", "additional directory configuration requires native precedence inspection", true)
			}
		}
		if sameDoctorPath(dir, projectRoot) || filepath.Dir(dir) == dir {
			break
		}
	}
	if trusted {
		layers = append(layers, project)
	} else {
		r.result("project trust", "UNVERIFIED", "confirm this project's trust and the exact hook in Codex /hooks", false)
	}
	enabled := true
	features := make(map[string]any)
	for _, layer := range layers {
		if profile, exists := layer["profile"]; exists && profile != nil {
			r.result("Codex profile", "UNVERIFIED", "selected profile requires native configuration inspection", true)
		}
		for _, key := range []string{"hooks", "codex_hooks"} {
			if value := doctorSetting(layer, "features", key); value != nil {
				features[key] = value
			}
		}
	}
	value := features["hooks"]
	if value == nil {
		value = features["codex_hooks"]
	}
	if value != nil {
		if flag, ok := value.(bool); ok {
			enabled = flag
		} else {
			r.result("hook feature", "INVALID", "hook feature flag must be a boolean", true)
		}
	}
	if doctorSetting(policy, "features", "hooks") == false || doctorSetting(policy, "features", "codex_hooks") == false {
		enabled = false
	}
	if !enabled || policy["allow_managed_hooks_only"] == true {
		r.result("hook activation", "DISABLED", "inspected settings exclude the repository hook", true)
	}
	hooksPath := filepath.Join(root, wiring.ConfigPath("codex"))
	data, readErr := readDoctorFile(hooksPath)
	var hooks map[string]any
	if readErr != nil || json.Unmarshal(data, &hooks) != nil {
		r.result("hook declaration", "INVALID", "hook configuration is unreadable or malformed", true)
	}
	if readErr == nil && json.Unmarshal(data, &hooks) == nil {
		groups, _ := doctorSetting(hooks, "hooks", "PreToolUse").([]any)
		canonical, _ := json.Marshal(wiring.CanonicalEntry("codex"))
		matched := false
		for index, raw := range groups {
			entry, _ := json.Marshal(raw)
			if !bytes.Equal(entry, canonical) {
				continue
			}
			matched = true
			key := fmt.Sprintf("%s:pre_tool_use:%d:0", hooksPath, index)
			state, _ := doctorSetting(user, "hooks", "state", key).(map[string]any)
			if flag, exists := state["enabled"]; exists {
				if _, ok := flag.(bool); !ok {
					r.result("hook state", "INVALID", "enabled must be a boolean", true)
				}
			}
			if state["enabled"] == false {
				r.result("hook state", "DISABLED", "user hook state disables this exact repository entry", true)
			}
			if state["trusted_hash"] == nil {
				r.result("hook trust", "UNTRUSTED", "no stored user trust for this entry; review it in Codex /hooks", false)
			} else {
				if _, ok := state["trusted_hash"].(string); !ok {
					r.result("hook trust", "INVALID", "stored trust hash must be a string", true)
				} else {
					r.result("hook trust", "UNVERIFIED", "stored trust cannot be compared with the native normalized definition; verify in /hooks", true)
				}
			}
		}
		if !matched {
			r.result("hook declaration", "UNVERIFIED", "no current canonical entry found; a fragment match alone cannot establish the command", true)
		}
	}
	r.result("effective session configuration", "UNVERIFIED", "profiles, ancestor layers, managed/cloud policy and invocation flags require native inspection", false)
	r.selectedRuntime()
	return r.finish()
}
