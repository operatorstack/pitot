package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	deepseek "github.com/operatorstack/pitot/integrations/deepseek"
	qwen "github.com/operatorstack/pitot/integrations/qwen"
	"github.com/operatorstack/pitot/wiring"
)

func priorityDoctorFixture(t *testing.T) (home, project string) {
	t.Helper()
	root := t.TempDir()
	home, project = filepath.Join(root, "home"), filepath.Join(root, "project")
	for _, p := range []string{home, project} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(project)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("QWEN_HOME", filepath.Join(home, ".qwen"))
	t.Setenv("QWEN_CODE_SYSTEM_SETTINGS_PATH", filepath.Join(root, "system", "settings.json"))
	t.Setenv("QWEN_CODE_SYSTEM_DEFAULTS_PATH", filepath.Join(root, "system", "system-defaults.json"))
	t.Setenv("QWEN_CODE_TRUSTED_FOLDERS_PATH", filepath.Join(home, ".qwen", "trustedFolders.json"))
	t.Setenv("PITOT_RUNTIME", "")
	t.Setenv("PITOT_BIN", "")
	t.Setenv("MSYSTEM", "")
	t.Setenv("SHELL", "")
	originalLookup, originalSystem := doctorLookPath, doctorCodexSystemDirectory
	doctorLookPath = func(string) (string, error) { return os.Args[0], nil }
	doctorCodexSystemDirectory = func() string { return filepath.Join(root, "codex-system") }
	t.Cleanup(func() { doctorLookPath, doctorCodexSystemDirectory = originalLookup, originalSystem })
	return home, project
}

func writeDoctorFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeDoctorJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeDoctorFixture(t, path, data)
}

func qwenFixtureSettings(t *testing.T, home string) map[string]any {
	t.Helper()
	bridge := filepath.Join(home, "hook scripts", "PreToolUse.cjs")
	writeDoctorFixture(t, bridge, []byte(qwen.NodeBridge))
	return map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{
		"matcher": "^run_shell_command$", "hooks": []any{map[string]any{"type": "command", "command": `node "` + bridge + `"`}},
	}}}}
}

func TestDoctorQwenStaticCases(t *testing.T) {
	for _, scenario := range []string{"configured", "absent", "malformed", "disabled", "reserved-enabled", "async", "wrong-matcher", "foreign-hook", "env-override", "sandbox", "missing-node", "dynamic-command", "missing-runtime"} {
		t.Run(scenario, func(t *testing.T) {
			home, _ := priorityDoctorFixture(t)
			settings := qwenFixtureSettings(t, home)
			hooks := settings["hooks"].(map[string]any)
			group := hooks["PreToolUse"].([]any)[0].(map[string]any)
			hook := group["hooks"].([]any)[0].(map[string]any)
			wantPass := scenario == "configured" || scenario == "reserved-enabled" || scenario == "foreign-hook"
			switch scenario {
			case "disabled":
				settings["disableAllHooks"] = true
			case "reserved-enabled":
				hooks["enabled"] = false
			case "async":
				hook["async"] = true
			case "wrong-matcher":
				group["matcher"] = "read_file|"
			case "foreign-hook":
				group["hooks"] = append(group["hooks"].([]any), map[string]any{"type": "command", "command": "echo audit"})
			case "env-override":
				hook["env"] = map[string]any{"PITOT_BIN": "private-sentinel-secret"}
			case "sandbox":
				settings["tools"] = map[string]any{"executionSandbox": map[string]any{"enabled": true}}
			case "missing-node":
				doctorLookPath = func(name string) (string, error) {
					if name == "node" {
						return "", exec.ErrNotFound
					}
					return os.Args[0], nil
				}
			case "dynamic-command":
				hook["command"] = `echo "private-sentinel-secret"; pitot hook qwen`
			case "missing-runtime":
				t.Setenv("PITOT_RUNTIME", filepath.Join(home, "absent-runtime.json"))
			}
			path := filepath.Join(home, ".qwen", "settings.json")
			if scenario != "absent" {
				writeDoctorJSON(t, path, settings)
			}
			if scenario == "malformed" {
				writeDoctorFixture(t, path, []byte(`{"private-sentinel-secret":`))
			}
			before, _ := os.ReadFile(path)
			var out bytes.Buffer
			err := doctorQwen(&out)
			if (err == nil) != wantPass {
				t.Fatalf("error=%v output=%s", err, out.String())
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) || strings.Contains(out.String(), "private-sentinel-secret") || strings.Contains(out.String(), "READY") {
				t.Fatalf("mutation, secret leak or false readiness: %s", out.String())
			}
			if !strings.Contains(out.String(), "live enforcement: UNVERIFIED") {
				t.Fatal("missing live enforcement boundary")
			}
		})
	}
}

func TestDoctorQwenSettingsPrecedenceAndTrust(t *testing.T) {
	for _, scenario := range []string{"system-disable", "workspace-enable", "untrusted", "deeper-deny", "home-root"} {
		t.Run(scenario, func(t *testing.T) {
			home, project := priorityDoctorFixture(t)
			settings := qwenFixtureSettings(t, home)
			settings["disableAllHooks"] = false
			writeDoctorJSON(t, filepath.Join(project, ".qwen", "settings.json"), settings)
			user := map[string]any{"disableAllHooks": true}
			if scenario == "untrusted" || scenario == "deeper-deny" {
				user["security"] = map[string]any{"folderTrust": map[string]any{"enabled": true}}
			}
			writeDoctorJSON(t, filepath.Join(home, ".qwen", "settings.json"), user)
			if scenario == "system-disable" {
				writeDoctorJSON(t, os.Getenv("QWEN_CODE_SYSTEM_SETTINGS_PATH"), map[string]any{"disableAllHooks": true})
			}
			if scenario == "deeper-deny" {
				writeDoctorJSON(t, os.Getenv("QWEN_CODE_TRUSTED_FOLDERS_PATH"), map[string]any{filepath.Dir(project): "TRUST_FOLDER", project: "DO_NOT_TRUST"})
			}
			if scenario == "home-root" {
				t.Chdir(home)
				t.Setenv("QWEN_HOME", filepath.Join(home, "different-qwen-home"))
				writeDoctorJSON(t, filepath.Join(home, ".qwen", "settings.json"), settings)
			}
			var out bytes.Buffer
			err := doctorQwen(&out)
			if (err == nil) != (scenario == "workspace-enable") {
				t.Fatalf("error=%v output=%s", err, out.String())
			}
		})
	}
}

func TestDoctorJSONAndMatcherBoundaries(t *testing.T) {
	for _, data := range []string{"\ufeff{/* comment */\"value\":\"https://host/*literal*/\"// comment\n}", "{\"value\":\"escaped\\\"//literal\"}"} {
		if _, err := doctorJSON([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	for _, data := range []string{"null", "[]", "{/* unfinished", "{\"x\":1,}"} {
		if _, err := doctorJSON([]byte(data)); err == nil {
			t.Fatalf("invalid JSON accepted: %s", data)
		}
	}
	for _, pattern := range []string{"read_file|", "^Bash$", "read_file"} {
		if match, _ := qwenShellMatcher(pattern); match {
			t.Fatalf("unrelated matcher accepted: %s", pattern)
		}
	}
	for _, pattern := range []string{" ^run_shell_command$ ", "Bash", "read_file|run_shell_command", "*"} {
		if match, known := qwenShellMatcher(pattern); !match || !known {
			t.Fatalf("supported matcher rejected: %s", pattern)
		}
	}
}

func codexFixture(t *testing.T, project string) {
	t.Helper()
	fragment, err := wiring.Fragment("codex")
	if err != nil {
		t.Fatal(err)
	}
	writeDoctorFixture(t, filepath.Join(project, wiring.FragmentPath("codex")), fragment)
	writeDoctorJSON(t, filepath.Join(project, wiring.ConfigPath("codex")), map[string]any{"hooks": map[string]any{"PreToolUse": []any{wiring.CanonicalEntry("codex")}}})
	writeDoctorFixture(t, filepath.Join(project, ".pitot", "version"), []byte("0.1.0\n"))
	path, content := filepath.Join(project, shimDir, "pitot"), shimPOSIX
	if runtime.GOOS == "windows" {
		path, content = filepath.Join(project, shimDir, "pitot.ps1"), shimPowerShell
	}
	writeDoctorFixture(t, path, []byte(content))
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorCodexStaticCases(t *testing.T) {
	for _, scenario := range []string{"configured", "missing-binary", "missing-shim", "disabled", "managed-only", "malformed", "hook-disabled", "profile", "alias-disable", "ancestor", "stored-trust", "invalid-state"} {
		t.Run(scenario, func(t *testing.T) {
			home, project := priorityDoctorFixture(t)
			codexFixture(t, project)
			user := ""
			switch scenario {
			case "missing-binary":
				doctorLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
			case "missing-shim":
				if err := os.RemoveAll(filepath.Join(project, shimDir)); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				user = "[features]\nhooks=false\n"
			case "managed-only":
				writeDoctorFixture(t, filepath.Join(doctorCodexSystemDirectory(), "requirements.toml"), []byte("allow_managed_hooks_only=true\n"))
			case "malformed":
				user = "private-sentinel-secret = ["
			case "hook-disabled":
				key := filepath.Join(project, wiring.ConfigPath("codex")) + ":pre_tool_use:0:0"
				user = "[hooks.state.'" + key + "']\nenabled=false\n"
			case "alias-disable":
				writeDoctorFixture(t, filepath.Join(doctorCodexSystemDirectory(), "config.toml"), []byte("[features]\nhooks=false\n"))
				user = "[features]\ncodex_hooks=true\n"
			case "ancestor":
				writeDoctorFixture(t, filepath.Join(filepath.Dir(project), ".git"), nil)
				writeDoctorFixture(t, filepath.Join(filepath.Dir(project), ".codex", "config.toml"), []byte("[features]\nhooks=false\n"))
			case "stored-trust", "invalid-state":
				key := filepath.Join(project, wiring.ConfigPath("codex")) + ":pre_tool_use:0:0"
				user = "[hooks.state.'" + key + "']\ntrusted_hash='stale'\n"
				if scenario == "invalid-state" {
					user += "enabled='false'\n"
				}
			case "profile":
				user = "profile='custom'\n"
			}
			path := filepath.Join(home, ".codex", "config.toml")
			writeDoctorFixture(t, path, []byte(user))
			var out bytes.Buffer
			err := doctorCodex(&out)
			if (err == nil) != (scenario == "configured") {
				t.Fatalf("error=%v output=%s", err, out.String())
			}
			after, _ := os.ReadFile(path)
			if string(after) != user || strings.Contains(out.String(), "private-sentinel-secret") || strings.Contains(out.String(), "READY") {
				t.Fatalf("mutation, secret leak or false readiness: %s", out.String())
			}
		})
	}
}

func TestDoctorDeepSeekCollectsIndependentFailures(t *testing.T) {
	priorityDoctorFixture(t)
	doctorLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	var out bytes.Buffer
	if err := doctorDeepSeek(&out); err == nil || !strings.Contains(out.String(), "binary dsh: MISSING") || !strings.Contains(out.String(), "native shell plugin: INVALID") {
		t.Fatalf("independent failures missing: error=%v output=%s", err, out.String())
	}
}

func TestDoctorQwenConflictingHookDefinitions(t *testing.T) {
	for _, scenario := range []string{"async", "env", "sandbox", "empty-name", "unknown-matcher"} {
		t.Run(scenario, func(t *testing.T) {
			home, project := priorityDoctorFixture(t)
			user := qwenFixtureSettings(t, home)
			workspace := qwenFixtureSettings(t, home)
			hook := workspace["hooks"].(map[string]any)["PreToolUse"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
			switch scenario {
			case "async":
				hook["async"] = true
			case "empty-name":
				hook["async"] = true
				hook["name"] = ""
			case "unknown-matcher":
				hook["async"] = true
				workspace["hooks"].(map[string]any)["PreToolUse"].([]any)[0].(map[string]any)["matcher"] = "^run_.*$"
			case "env":
				hook["env"] = map[string]any{"PITOT_BIN": "unavailable"}
			case "sandbox":
				user["tools"] = map[string]any{"executionSandbox": true}
				workspace["tools"] = map[string]any{"executionSandbox": false}
			}
			writeDoctorJSON(t, filepath.Join(home, ".qwen", "settings.json"), user)
			writeDoctorJSON(t, filepath.Join(project, ".qwen", "settings.json"), workspace)
			var out bytes.Buffer
			if err := doctorQwen(&out); err == nil {
				t.Fatalf("false static pass: %s", out.String())
			}
		})
	}
}

func TestDoctorSelectedRuntime(t *testing.T) {
	for _, scenario := range []string{"unset", "healthy", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			home, _ := priorityDoctorFixture(t)
			switch scenario {
			case "healthy":
				path, _, _ := readinessFixture(t, false)
				t.Setenv("PITOT_RUNTIME", path)
			case "malformed":
				path := filepath.Join(home, "runtime.json")
				writeDoctorFixture(t, path, []byte(`{"token":"private-sentinel-secret"`))
				t.Setenv("PITOT_RUNTIME", path)
			}
			var out bytes.Buffer
			report := &readinessReport{out: &out}
			report.selectedRuntime()
			if (report.issues != 0) != (scenario == "malformed") {
				t.Fatalf("unexpected issues: %s", out.String())
			}
			if strings.Contains(out.String(), "private-sentinel-secret") {
				t.Fatal("secret disclosed")
			}
			if scenario == "healthy" && !strings.Contains(out.String(), "Controller availability was not checked") {
				t.Fatalf("missing scope: %s", out.String())
			}
		})
	}
}

func TestDoctorCodexExcludesConfigurationAboveRepository(t *testing.T) {
	home, project := priorityDoctorFixture(t)
	codexFixture(t, project)
	writeDoctorFixture(t, filepath.Join(project, ".git"), nil)
	writeDoctorFixture(t, filepath.Join(filepath.Dir(project), ".codex", "config.toml"), []byte("[features]\nhooks=false\n"))
	writeDoctorFixture(t, filepath.Join(home, ".codex", "config.toml"), []byte("[features]\nhooks=true\n"))
	var out bytes.Buffer
	if err := doctorCodex(&out); err != nil {
		t.Fatalf("unexpected failure: %v\n%s", err, out.String())
	}
}

func doctorTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[path] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDoctorReadOnlyAndExplicitFix(t *testing.T) {
	home, project := priorityDoctorFixture(t)
	codexFixture(t, project)
	settings := qwenFixtureSettings(t, home)
	sentinel := filepath.Join(project, "hook-was-executed")
	group := settings["hooks"].(map[string]any)["PreToolUse"].([]any)[0].(map[string]any)
	group["hooks"] = append(group["hooks"].([]any), map[string]any{"type": "command", "command": `echo secret > "` + sentinel + `"`})
	writeDoctorJSON(t, filepath.Join(home, ".qwen", "settings.json"), settings)
	before := doctorTree(t, filepath.Dir(project))
	var out bytes.Buffer
	if err := doctorQwen(&out); err != nil {
		t.Fatalf("Qwen: %v\n%s", err, out.String())
	}
	if err := doctorCodex(&out); err != nil {
		t.Fatalf("Codex: %v\n%s", err, out.String())
	}
	if !reflect.DeepEqual(before, doctorTree(t, filepath.Dir(project))) {
		t.Fatal("plain doctor modified configuration or executed a hook")
	}
	hooksPath := filepath.Join(project, wiring.ConfigPath("codex"))
	writeDoctorJSON(t, hooksPath, map[string]any{"theme": "foreign-setting", "hooks": map[string]any{"PreToolUse": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "foreign-command"}}}}}})
	doctorLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	if err := doctor([]string{"--host", "codex", "--fix"}, &out, &out); err == nil {
		t.Fatal("repair masked missing executable")
	}
	state, err := wiring.Check(project, "codex")
	if err != nil || state != wiring.StateFound {
		t.Fatalf("repair did not restore wiring: %s %v", state, err)
	}
	data, err := os.ReadFile(hooksPath)
	if err != nil || !bytes.Contains(data, []byte("foreign-command")) || !bytes.Contains(data, []byte("foreign-setting")) {
		t.Fatal("repair removed foreign settings")
	}
	if !strings.Contains(out.String(), "checking static setup separately") {
		t.Fatal("repair was not distinguished from verification")
	}
}

func TestDoctorDeepSeekVersionAndPlugin(t *testing.T) {
	// This executable is a local metadata fixture, not a real agent or model.
	root := t.TempDir()
	source := filepath.Join(root, "version.go")
	body := fmt.Sprintf(`package main
import("fmt";"os";"strings")
func main(){if len(os.Args)!=2 || os.Args[1]!="--version" {os.Exit(3)}; if strings.Contains(os.Args[0],"bad-version") {fmt.Fprintln(os.Stderr,"private-sentinel-secret");os.Exit(2)};fmt.Println(%q)}
`, deepseek.Version)
	writeDoctorFixture(t, source, []byte(body))
	program := filepath.Join(root, "dsh-fixture")
	if runtime.GOOS == "windows" {
		program += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", program, source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build metadata fixture: %v\n%s", err, output)
	}
	for _, bad := range []bool{false, true} {
		t.Run(fmt.Sprint(bad), func(t *testing.T) {
			_, project := priorityDoctorFixture(t)
			selected := program
			if bad {
				selected = filepath.Join(root, "bad-version"+filepath.Ext(program))
				data, err := os.ReadFile(program)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(selected, data, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				for path, data := range deepSeekOwnedFiles() {
					writeDoctorFixture(t, filepath.Join(project, filepath.FromSlash(path)), data)
				}
			}
			doctorLookPath = func(string) (string, error) { return selected, nil }
			before := doctorTree(t, filepath.Dir(project))
			var out bytes.Buffer
			err := doctorDeepSeek(&out)
			if (err != nil) != bad {
				t.Fatalf("unexpected diagnostic: %v\n%s", err, out.String())
			}
			if bad && (!strings.Contains(out.String(), "DeepSeek version: UNVERIFIED") || !strings.Contains(out.String(), "native shell plugin: INVALID")) {
				t.Fatal("independent failures hidden")
			}
			if strings.Contains(out.String(), "private-sentinel-secret") || !reflect.DeepEqual(before, doctorTree(t, filepath.Dir(project))) {
				t.Fatal("secret disclosure or profile mutation")
			}
		})
	}
}

func TestDoctorQwenRetainsHookLayers(t *testing.T) {
	home, _ := priorityDoctorFixture(t)
	defaults := qwenFixtureSettings(t, home)
	writeDoctorJSON(t, os.Getenv("QWEN_CODE_SYSTEM_DEFAULTS_PATH"), defaults)
	writeDoctorJSON(t, os.Getenv("QWEN_CODE_SYSTEM_SETTINGS_PATH"), map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "read_file", "hooks": []any{map[string]any{"type": "command", "command": "foreign"}}}}}})
	var out bytes.Buffer
	if err := doctorQwen(&out); err != nil {
		t.Fatalf("system hooks replaced default route: %v\n%s", err, out.String())
	}
}
