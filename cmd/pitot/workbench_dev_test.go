package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
	"testing"
)

// decisionLine models one entry from `pitot dev`'s decision timeline.
type decisionLine struct {
	outcome  string // ALLOW or DENY
	kind     string
	actionID string
	message  string
}

var decisionRE = regexp.MustCompile(`\[(ALLOW|DENY)\]\s+(\S+)\s+\((act_[^)]+)\)(?:\s+\p{Pd}+\s+(.*))?`)

// parseDecisions extracts the decision timeline rendered by `pitot dev` (and the
// real-Kimi smoke test) from a captured stdout stream.
func parseDecisions(output string) []decisionLine {
	var out []decisionLine
	for _, m := range decisionRE.FindAllStringSubmatch(output, -1) {
		out = append(out, decisionLine{outcome: m[1], kind: m[2], actionID: m[3], message: strings.TrimSpace(m[4])})
	}
	return out
}

// buildPitotBinary compiles the reference CLI from the in-tree package so tests
// can invoke `pitot hook`/`pitot dev` as a real subprocess. Callers must invoke
// this before any t.Chdir, since it builds from the package's working directory.
func buildPitotBinary(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	name := "pitot"
	if goruntime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(binDir, name)
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build pitot binary: %v\n%s", err, out)
	}
	return bin
}

// devAgentScript writes a POSIX agent stand-in that records the runtime it was
// handed and its own argv, then drives one allow and one deny decision through
// the running runtime via `pitot hook kimi`. It stands in for a real coding
// agent so the dev harness can be exercised deterministically.
func devAgentScript(t *testing.T, seenDir string) string {
	t.Helper()
	allowPayload := `{"hook_event_name":"PreToolUse","session_id":"dev","tool_name":"Bash","tool_input":{"command":"echo ok"}}`
	denyPayload := `{"hook_event_name":"PreToolUse","session_id":"dev","tool_name":"Bash","tool_input":{"command":"PITOT_DENY_ME=1 echo no"}}`
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s' "$PITOT_RUNTIME" > %q
printf '%%s' "$#" > %q
printf '%%s' "$*" > %q
printf '%s' | "$PITOT_BIN" hook kimi --runtime "$PITOT_RUNTIME"
printf '%s' | "$PITOT_BIN" hook kimi --runtime "$PITOT_RUNTIME" || true
`,
		filepath.Join(seenDir, "runtime.txt"),
		filepath.Join(seenDir, "argc.txt"),
		filepath.Join(seenDir, "args.txt"),
		allowPayload, denyPayload)
	path := filepath.Join(seenDir, "agent.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDevRunsAgentBehindShellController is the end-to-end proof of the dev
// harness: it starts the runtime + the compiled shell-policy controller, launches
// a scripted agent, and verifies the decision timeline, PITOT_RUNTIME
// propagation, argv preservation, and runtime-dir teardown.
func TestDevRunsAgentBehindShellController(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("dev agent stand-in is a POSIX shell script")
	}
	// Build inputs before we chdir into the project directory.
	pitotBin := buildPitotBinary(t)
	_, configBody := buildGeneratedShellPolicy(t)

	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, ".pitot.yaml"), []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	seenDir := t.TempDir()
	agent := devAgentScript(t, seenDir)

	t.Setenv("PITOT_BIN", pitotBin)
	t.Setenv("PITOT_RUNTIME", "")
	t.Chdir(proj)

	var stdout lockedBuffer
	var stderr lockedBuffer
	// `-- sh AGENT a b` exercises the explicit argv form (args after --).
	if err := runDev(context.Background(), []string{"--host", "kimi", "--", "sh", agent, "alpha", "beta"}, &stdout, &stderr); err != nil {
		t.Fatalf("pitot dev: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	// Decision timeline must show exactly one allow and one deny, both shell-kind,
	// and the deny must carry the Controller's canary reason.
	decisions := parseDecisions(stdout.String())
	var gotAllow, gotDeny bool
	for _, d := range decisions {
		if d.kind != "shell" {
			t.Errorf("decision kind = %q, want shell: %+v", d.kind, d)
		}
		switch d.outcome {
		case "ALLOW":
			gotAllow = true
		case "DENY":
			gotDeny = true
			if !strings.Contains(d.message, "PITOT_DENY_ME canary") {
				t.Errorf("deny message missing Controller reason: %q", d.message)
			}
		}
	}
	if !gotAllow || !gotDeny {
		t.Fatalf("decision timeline missing allow+deny (allow=%v deny=%v):\n%s", gotAllow, gotDeny, stdout.String())
	}

	// The agent must have received a real PITOT_RUNTIME pointing at the per-run dir.
	runtimeSeen := readFileString(t, filepath.Join(seenDir, "runtime.txt"))
	if !strings.Contains(runtimeSeen, "pitot-dev-") {
		t.Errorf("agent PITOT_RUNTIME = %q, want a per-invocation dev runtime path", runtimeSeen)
	}
	// argv after `--` is preserved verbatim: two positional args, joined "alpha beta".
	if argc := readFileString(t, filepath.Join(seenDir, "argc.txt")); argc != "2" {
		t.Errorf("agent argc = %q, want 2", argc)
	}
	if args := readFileString(t, filepath.Join(seenDir, "args.txt")); args != "alpha beta" {
		t.Errorf("agent args = %q, want \"alpha beta\"", args)
	}
	// The per-invocation runtime directory must be removed once dev exits.
	if _, err := os.Stat(filepath.Dir(runtimeSeen)); !os.IsNotExist(err) {
		t.Errorf("runtime dir %q must be removed on exit, stat err=%v", filepath.Dir(runtimeSeen), err)
	}
}

// TestDevRuntimePathsAreUniquePerRun confirms concurrent-safe isolation: two dev
// runs of the same project hand the agent two different runtime descriptors.
func TestDevRuntimePathsAreUniquePerRun(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("dev agent stand-in is a POSIX shell script")
	}
	pitotBin := buildPitotBinary(t)
	_, configBody := buildGeneratedShellPolicy(t)

	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, ".pitot.yaml"), []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PITOT_BIN", pitotBin)
	t.Setenv("PITOT_RUNTIME", "")
	t.Chdir(proj)

	run := func() string {
		seenDir := t.TempDir()
		agent := devAgentScript(t, seenDir)
		var stdout lockedBuffer
		var stderr lockedBuffer
		if err := runDev(context.Background(), []string{"--host", "kimi", "--", "sh", agent}, &stdout, &stderr); err != nil {
			t.Fatalf("pitot dev: %v\n%s", err, stderr.String())
		}
		return readFileString(t, filepath.Join(seenDir, "runtime.txt"))
	}
	first, second := run(), run()
	if first == "" || first == second {
		t.Errorf("runtime paths not unique per run: %q vs %q", first, second)
	}
}

// TestDevExecSplitsWhereArgvDoesNot pins the distinct semantics of the two agent
// forms: --exec field-splits its string, while `-- CMD ARGS` preserves argv.
func TestDevExecSplitsWhereArgvDoesNot(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("dev agent stand-in is a POSIX shell script")
	}
	pitotBin := buildPitotBinary(t)
	_, configBody := buildGeneratedShellPolicy(t)

	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, ".pitot.yaml"), []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PITOT_BIN", pitotBin)
	t.Setenv("PITOT_RUNTIME", "")
	t.Chdir(proj)

	argc := func(devArgs []string) string {
		seenDir := t.TempDir()
		agent := devAgentScript(t, seenDir)
		// Substitute the agent path into the caller's arg template.
		filled := make([]string, len(devArgs))
		for i, a := range devArgs {
			filled[i] = strings.ReplaceAll(a, "{AGENT}", agent)
		}
		var stdout lockedBuffer
		var stderr lockedBuffer
		if err := runDev(context.Background(), filled, &stdout, &stderr); err != nil {
			t.Fatalf("pitot dev %v: %v\n%s", filled, err, stderr.String())
		}
		return readFileString(t, filepath.Join(seenDir, "argc.txt"))
	}

	// --exec "sh AGENT a b" splits on whitespace -> sh sees 2 positional args.
	if got := argc([]string{"--host", "kimi", "--exec", "sh {AGENT} a b"}); got != "2" {
		t.Errorf("--exec field-split argc = %q, want 2", got)
	}
	// -- sh AGENT "a b" preserves argv -> sh sees 1 positional arg.
	if got := argc([]string{"--host", "kimi", "--", "sh", "{AGENT}", "a b"}); got != "1" {
		t.Errorf("-- argv-preserving argc = %q, want 1", got)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
