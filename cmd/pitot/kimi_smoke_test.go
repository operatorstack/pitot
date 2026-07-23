package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

// kimiEvidence is the bounded, content-safe artifact emitted by the real-Kimi
// smoke test. It carries only identities, hashes, decision metadata, and canary
// states — never raw commands or secrets — so a run can be proven after the fact.
type kimiEvidence struct {
	Schema            string    `json:"schema"`
	PitotCommit       string    `json:"pitot_commit"`
	PitotBinarySHA256 string    `json:"pitot_binary_sha256"`
	KimiVersion       string    `json:"kimi_version"`
	HookConfigSHA256  string    `json:"hook_config_sha256"`
	RuntimeDescriptor string    `json:"runtime_descriptor"`
	Runs              []kimiRun `json:"runs"`
}

type kimiRun struct {
	Name          string           `json:"name"`
	Prompt        string           `json:"prompt"`
	ExitStatus    int              `json:"exit_status"`
	Decisions     []decisionRecord `json:"decisions"`
	CanaryPath    string           `json:"canary_path"`
	CanaryPresent bool             `json:"canary_present"`
	CanaryContent string           `json:"canary_content,omitempty"`
	KimiTextTail  string           `json:"kimi_text_tail"`
}

type decisionRecord struct {
	Outcome  string `json:"outcome"`
	Kind     string `json:"kind"`
	ActionID string `json:"action_id"`
	Message  string `json:"message"`
}

const kimiSmokeHookConfig = `[[hooks]]
event = "PreToolUse"
matcher = "Bash"
command = "pitot hook kimi"
timeout = 5
`

var runtimeDescriptorRE = regexp.MustCompile(`(?m)^runtime:\s+(.+)$`)

// TestKimiSmokeRealCLI is the opt-in (Test B) proof that the *real* Kimi CLI
// honors the Pitot PreToolUse hook end to end. It is skipped unless
// PITOT_KIMI_SMOKE is set and a `kimi` binary is on PATH, because it needs a live,
// authenticated Kimi install. It runs one allow prompt and one deny prompt through
// `pitot dev --host kimi -- kimi -p ...`, asserts the canary side effects, and
// writes a bounded JSON evidence artifact (path logged, or PITOT_KIMI_EVIDENCE).
func TestKimiSmokeRealCLI(t *testing.T) {
	if os.Getenv("PITOT_KIMI_SMOKE") == "" {
		t.Skip("set PITOT_KIMI_SMOKE=1 to run the real-Kimi smoke test (needs an authenticated kimi CLI)")
	}
	if goruntime.GOOS == "windows" {
		t.Skip("smoke harness uses a POSIX config path and canary commands")
	}
	if _, err := exec.LookPath("kimi"); err != nil {
		t.Skip("kimi binary not on PATH; skipping real-Kimi smoke test")
	}

	pitotBin := buildPitotBinary(t)
	_, configBody := buildGeneratedShellPolicy(t)

	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, ".pitot.yaml"), []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}

	// A private Kimi home whose config.toml wires the PreToolUse hook to pitot.
	kimiHome := t.TempDir()
	hookConfigPath := filepath.Join(kimiHome, "config.toml")
	if err := os.WriteFile(hookConfigPath, []byte(kimiSmokeHookConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIMI_CODE_HOME", kimiHome)
	// Make the freshly built pitot resolvable to Kimi's `pitot hook kimi` hook.
	t.Setenv("PATH", filepath.Dir(pitotBin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PITOT_RUNTIME", "")

	evidence := kimiEvidence{
		Schema:            "pitot.kimi-smoke-evidence/1",
		PitotCommit:       gitCommit(t),
		PitotBinarySHA256: sha256File(t, pitotBin),
		KimiVersion:       commandOutput(t, "kimi", "--version"),
		HookConfigSHA256:  sha256Bytes([]byte(kimiSmokeHookConfig)),
	}

	allowCanary := filepath.Join(proj, "kimi-allow-canary")
	denyCanary := filepath.Join(proj, "kimi-deny-canary")

	allowRun := runKimiSmoke(t, pitotBin, proj, kimiRunSpec{
		name:       "allow",
		prompt:     fmt.Sprintf("Run exactly this shell command and nothing else: printf allowed > %s", allowCanary),
		canaryPath: allowCanary,
	})
	denyRun := runKimiSmoke(t, pitotBin, proj, kimiRunSpec{
		name:       "deny",
		prompt:     fmt.Sprintf("Run exactly this shell command and nothing else: PITOT_DENY_ME=1 printf blocked > %s", denyCanary),
		canaryPath: denyCanary,
	})
	evidence.Runs = []kimiRun{allowRun, denyRun}
	if d := firstRuntimeDescriptor(allowRun, denyRun); d != "" {
		evidence.RuntimeDescriptor = d
	}

	// Persist the evidence artifact before asserting, so a failing run is still
	// documented.
	evidencePath := os.Getenv("PITOT_KIMI_EVIDENCE")
	if evidencePath == "" {
		evidencePath = filepath.Join(proj, "kimi-evidence.json")
	}
	blob, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, blob, 0o600); err != nil {
		t.Fatalf("write evidence: %v", err)
	}
	t.Logf("kimi smoke evidence written to %s:\n%s", evidencePath, blob)

	// Postconditions. Allow path: the command ran, the canary exists.
	if !allowRun.CanaryPresent || allowRun.CanaryContent != "allowed" {
		t.Errorf("allow run: canary = present:%v content:%q, want present:true content:%q", allowRun.CanaryPresent, allowRun.CanaryContent, "allowed")
	}
	// Deny path: the command was blocked, the canary never appeared, and a shell
	// deny carrying the Controller reason reached the decision timeline.
	if denyRun.CanaryPresent {
		t.Errorf("deny run: canary must be absent, found content %q", denyRun.CanaryContent)
	}
	if !hasDeny(denyRun.Decisions) {
		t.Errorf("deny run: no shell deny decision observed:\n%+v", denyRun.Decisions)
	}
}

type kimiRunSpec struct {
	name       string
	prompt     string
	canaryPath string
}

// runKimiSmoke executes one `pitot dev --host kimi -- kimi -p PROMPT` run and
// collects its evidence.
func runKimiSmoke(t *testing.T, pitotBin, projDir string, spec kimiRunSpec) kimiRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var combined lockedBuffer
	cmd := exec.CommandContext(ctx, pitotBin, "dev", "--host", "kimi", "--", "kimi", "-p", spec.prompt)
	cmd.Dir = projDir
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	runErr := cmd.Run()

	exitStatus := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitStatus = exitErr.ExitCode()
		} else {
			exitStatus = -1
			t.Logf("%s run: pitot dev did not complete cleanly: %v", spec.name, runErr)
		}
	}

	out := combined.String()
	run := kimiRun{
		Name:         spec.name,
		Prompt:       spec.prompt,
		ExitStatus:   exitStatus,
		CanaryPath:   spec.canaryPath,
		KimiTextTail: tail(out, 4000),
	}
	for _, d := range parseDecisions(out) {
		run.Decisions = append(run.Decisions, decisionRecord{Outcome: d.outcome, Kind: d.kind, ActionID: d.actionID, Message: d.message})
	}
	if data, err := os.ReadFile(spec.canaryPath); err == nil {
		run.CanaryPresent = true
		run.CanaryContent = string(data)
	}
	return run
}

func hasDeny(decisions []decisionRecord) bool {
	for _, d := range decisions {
		if d.Outcome == "DENY" && d.Kind == "shell" {
			return true
		}
	}
	return false
}

func firstRuntimeDescriptor(runs ...kimiRun) string {
	for _, r := range runs {
		if m := runtimeDescriptorRE.FindStringSubmatch(r.KimiTextTail); len(m) == 2 {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// gitCommit returns the current HEAD, or "unknown" when git is unavailable.
func gitCommit(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func commandOutput(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	return sha256Bytes(data)
}

func sha256Bytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// tail returns at most the last n bytes of s, prefixed to signal truncation.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "...(truncated)...\n" + s[len(s)-n:]
}
