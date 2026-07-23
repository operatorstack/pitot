package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

// buildGeneratedShellPolicy scaffolds a shell-policy Go controller with `pitot
// init`, points it at the in-tree module with a filesystem replace, and compiles
// it offline. This proves the *generated* sample Controller builds and runs
// without a published SDK dependency (Definition of Done), rather than testing a
// hand-written stand-in.
func buildGeneratedShellPolicy(t *testing.T) (binPath, configWithBinary string) {
	t.Helper()

	proj := filepath.Join(t.TempDir(), "shell-policy-proj")
	var out, errb bytes.Buffer
	if err := runInit([]string{"--language", "go", "--template", "shell-policy", "--dir", proj}, strings.NewReader(""), &out, &errb); err != nil {
		t.Fatalf("init shell-policy: %v\n%s", err, errb.String())
	}

	// The generated config must register the controller under the shell kind.
	cfg, err := os.ReadFile(filepath.Join(proj, ".pitot.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "shell:") || !strings.Contains(string(cfg), "local-shell-policy") {
		t.Fatalf("generated config does not register the shell-policy controller under shell:\n%s", cfg)
	}

	// Resolve the in-tree module root (cmd/pitot -> module root) and add a
	// filesystem replace so the generated project resolves the SDK locally.
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	gomod := filepath.Join(proj, "go.mod")
	existing, err := os.ReadFile(gomod)
	if err != nil {
		t.Fatal(err)
	}
	replace := fmt.Sprintf("\nreplace github.com/operatorstack/pitot => %s\n", moduleRoot)
	if err := os.WriteFile(gomod, append(existing, []byte(replace)...), 0o644); err != nil {
		t.Fatal(err)
	}

	name := "shell-policy"
	if goruntime.GOOS == "windows" {
		name += ".exe"
	}
	binPath = filepath.Join(proj, name)
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = proj
	build.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build generated shell-policy controller: %v\n%s", err, output)
	}

	// A config that runs the compiled controller by absolute path, avoiding a
	// per-spawn `go run` compile and any working-directory coupling.
	configWithBinary = fmt.Sprintf(`controllers:
  shell:
    id: local-shell-policy
    command: [%q]
    deadline_ms: 2000
    on_timeout: deny
    on_unavailable: deny
`, binPath)
	return binPath, configWithBinary
}

// TestKimiShellPolicyAllowAndDeny is the deterministic (no-model) proof of the
// controlled-action path: a canonical Kimi PreToolUse/Bash payload is allowed
// and its canary runs, while the PITOT_DENY_ME payload is blocked (exit 2), its
// canary never runs, and the Controller's reason reaches the caller.
func TestKimiShellPolicyAllowAndDeny(t *testing.T) {
	if goruntime.GOOS == "windows" {
		// The canary harness drives POSIX shell commands (`sh -c`, `printf`,
		// `VAR=1 sh -c ...`) with Unix path semantics; Git Bash on Windows mangles
		// the backslashed temp paths. The control path itself is exercised on the
		// POSIX runners.
		t.Skip("canary harness uses POSIX shell commands and Unix path semantics")
	}
	t.Setenv("PITOT_RUNTIME", "")

	_, configBody := buildGeneratedShellPolicy(t)

	dir := t.TempDir()
	configPath := filepath.Join(dir, ".pitot.yaml")
	runtimePath := filepath.Join(dir, "runtime.json")
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}

	// Per-test canary paths keep the test hermetic (no shared /tmp files).
	allowedCanary := filepath.Join(dir, "pitot-allowed-canary")
	deniedCanary := filepath.Join(dir, "pitot-denied-canary")

	// Start the runtime backing the shell controller.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var runtimeOut bytes.Buffer
	var runtimeErr lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- runWithIO(ctx, []string{"run", "--config", configPath, "--runtime", runtimePath}, strings.NewReader(""), &runtimeOut, &runtimeErr)
	}()
	waitForRuntimeFile(t, runtimePath, &runtimeErr)

	// --- Allow path: hook returns 0, then the harness executes the canary. ---
	allowCmd := fmt.Sprintf("printf allowed > %s", allowedCanary)
	allowPayload := fmt.Sprintf(`{"hook_event_name":"PreToolUse","session_id":"pitot-local-test","tool_name":"Bash","tool_input":{"command":%q}}`, allowCmd)
	var allowOut, allowErrOut bytes.Buffer
	allowErr := runWithIO(context.Background(), []string{"hook", "kimi", "--runtime", runtimePath}, strings.NewReader(allowPayload), &allowOut, &allowErrOut)
	if allowErr != nil {
		t.Fatalf("allow hook: expected exit 0, got err=%v stderr=%s", allowErr, allowErrOut.String())
	}
	if !strings.Contains(allowOut.String(), `"kind":"shell"`) {
		t.Errorf("allow receipt missing kind=shell:\n%s", allowOut.String())
	}
	// Host execution is simulated ONLY after an allow decision.
	runCanary(t, allowCmd)
	if got, err := os.ReadFile(allowedCanary); err != nil || string(got) != "allowed" {
		t.Fatalf("allowed canary: want %q, got %q (err=%v)", "allowed", string(got), err)
	}

	// --- Deny path: hook returns exit 2 and the canary is never executed. ---
	denyCmd := fmt.Sprintf("PITOT_DENY_ME=1 sh -c 'printf blocked > %s'", deniedCanary)
	denyPayload := fmt.Sprintf(`{"hook_event_name":"PreToolUse","session_id":"pitot-local-test","tool_name":"Bash","tool_input":{"command":%q}}`, denyCmd)
	var denyOut, denyErrOut bytes.Buffer
	denyErr := runWithIO(context.Background(), []string{"hook", "kimi", "--runtime", runtimePath}, strings.NewReader(denyPayload), &denyOut, &denyErrOut)
	if !errors.Is(denyErr, errBlocked) {
		t.Fatalf("deny hook: expected errBlocked (exit 2), got err=%v stdout=%s stderr=%s", denyErr, denyOut.String(), denyErrOut.String())
	}
	if !strings.Contains(denyOut.String(), `"kind":"shell"`) {
		t.Errorf("deny receipt missing kind=shell:\n%s", denyOut.String())
	}
	// The Controller's exact reason must reach the caller (Kimi) via stderr.
	if !strings.Contains(denyErrOut.String(), "PITOT_DENY_ME canary") {
		t.Errorf("deny stderr missing Controller reason:\n%s", denyErrOut.String())
	}
	// Because the hook denied, the harness must NOT execute the command.
	if denyErr == nil {
		runCanary(t, denyCmd)
	}
	if _, err := os.Stat(deniedCanary); !os.IsNotExist(err) {
		t.Fatalf("denied canary must be absent, stat err=%v", err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// waitForRuntimeFile blocks until the runtime descriptor is published.
func waitForRuntimeFile(t *testing.T, runtimePath string, runtimeErr *lockedBuffer) {
	t.Helper()
	for i := 0; i < 250; i++ {
		if _, err := os.Stat(runtimePath); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("runtime did not become ready:\n%s", runtimeErr.String())
}

// runCanary simulates the host executing an allowed command. It is only ever
// invoked after an allow decision, mirroring how a real host runs a command the
// PreToolUse hook approved.
func runCanary(t *testing.T, command string) {
	t.Helper()
	if output, err := exec.Command("sh", "-c", command).CombinedOutput(); err != nil {
		t.Fatalf("canary command %q failed: %v\n%s", command, err, output)
	}
}
