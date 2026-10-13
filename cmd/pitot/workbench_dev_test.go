package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
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

// writeFragment registers a tenant config fragment under root/.pitot/conf.d.
func writeFragment(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, ".pitot", "conf.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// buildDevAgent compiles a shell-free helper before the test changes directory.
func buildDevAgent(t *testing.T) string {
	t.Helper()
	name := "dev-agent"
	if goruntime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	if out, err := exec.Command("go", "build", "-o", path, "./testdata/dev-agent").CombinedOutput(); err != nil {
		t.Fatalf("build dev agent: %v\n%s", err, out)
	}
	return path
}

// TestDevRunsAgentBehindShellController is the end-to-end proof of the dev
// harness: it starts the runtime + the compiled shell-policy controller, launches
// a native agent stand-in, and verifies the decision timeline, PITOT_RUNTIME
// propagation, argv preservation, and runtime-dir teardown.
func TestDevRunsAgentBehindShellController(t *testing.T) {
	// Build inputs before we chdir into the project directory.
	pitotBin := buildPitotBinary(t)
	agent := buildDevAgent(t)
	_, configBody := buildGeneratedShellPolicy(t)

	proj := t.TempDir()
	writeFragment(t, proj, "shell-policy", configBody)
	seenDir := t.TempDir()
	t.Setenv("PITOT_TEST_SEEN", seenDir)

	t.Setenv("PATH", filepath.Dir(agent)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PITOT_BIN", pitotBin)
	t.Setenv("PITOT_RUNTIME", "stale-descriptor")
	t.Chdir(proj)

	wantedArgs := []string{"", "alpha beta", `say "hi"`, `C:\a\`, "你好", "&|<>^%!()"}
	var stdout lockedBuffer
	var stderr lockedBuffer
	// The explicit argv form must preserve arguments without shell interpolation.
	if err := runDev(context.Background(), append([]string{"--host", "qwen", "--", agent}, wantedArgs...), &stdout, &stderr); err != nil {
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
	var gotArgs []string
	if err := json.Unmarshal([]byte(readFileString(t, filepath.Join(seenDir, "args.json"))), &gotArgs); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotArgs, wantedArgs) {
		t.Errorf("argv = %#v, want %#v", gotArgs, wantedArgs)
	}
	// The per-invocation runtime directory must be removed once dev exits.
	if _, err := os.Stat(filepath.Dir(runtimeSeen)); !os.IsNotExist(err) {
		t.Errorf("runtime dir %q must be removed on exit, stat err=%v", filepath.Dir(runtimeSeen), err)
	}
}

// TestDevRuntimePathsAreUniquePerRun confirms concurrent-safe isolation: two dev
// runs of the same project hand the agent two different runtime descriptors.
func TestDevRuntimePathsAreUniquePerRun(t *testing.T) {
	pitotBin := buildPitotBinary(t)
	agent := buildDevAgent(t)
	_, configBody := buildGeneratedShellPolicy(t)

	proj := t.TempDir()
	writeFragment(t, proj, "shell-policy", configBody)
	t.Setenv("PATH", filepath.Dir(agent)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PITOT_BIN", pitotBin)
	t.Setenv("PITOT_RUNTIME", "stale-descriptor")
	t.Chdir(proj)

	run := func() string {
		seenDir := t.TempDir()
		t.Setenv("PITOT_TEST_SEEN", seenDir)
		var stdout lockedBuffer
		var stderr lockedBuffer
		if err := runDev(context.Background(), []string{"--host", "qwen", "--", agent}, &stdout, &stderr); err != nil {
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
	pitotBin := buildPitotBinary(t)
	agent := buildDevAgent(t)
	_, configBody := buildGeneratedShellPolicy(t)

	proj := t.TempDir()
	writeFragment(t, proj, "shell-policy", configBody)
	t.Setenv("PATH", filepath.Dir(agent)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PITOT_BIN", pitotBin)
	t.Setenv("PITOT_RUNTIME", "stale-descriptor")
	t.Chdir(proj)

	argc := func(devArgs []string) string {
		seenDir := t.TempDir()
		t.Setenv("PITOT_TEST_SEEN", seenDir)
		// Substitute the agent path into the caller's arg template.
		filled := make([]string, len(devArgs))
		for i, a := range devArgs {
			filled[i] = strings.ReplaceAll(a, "{AGENT}", filepath.Base(agent))
		}
		var stdout lockedBuffer
		var stderr lockedBuffer
		if err := runDev(context.Background(), filled, &stdout, &stderr); err != nil {
			t.Fatalf("pitot dev %v: %v\n%s", filled, err, stderr.String())
		}
		return readFileString(t, filepath.Join(seenDir, "argc.txt"))
	}

	// --exec splits on whitespace.
	if got := argc([]string{"--host", "qwen", "--exec", "{AGENT} a b"}); got != "2" {
		t.Errorf("--exec field-split argc = %q, want 2", got)
	}
	// The explicit form preserves one argument containing a space.
	if got := argc([]string{"--host", "qwen", "--", "{AGENT}", "a b"}); got != "1" {
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

func TestDevFailureCleanup(t *testing.T) {
	agent := buildDevAgent(t)
	_, configBody := buildGeneratedShellPolicy(t)
	proj := t.TempDir()
	writeFragment(t, proj, "shell-policy", configBody)
	t.Chdir(proj)
	for _, mode := range []string{"missing", "fail", "cancel", "pipe", "record"} {
		t.Run(mode, func(t *testing.T) {
			seen := t.TempDir()
			tempRoot := t.TempDir()
			for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
				t.Setenv(name, tempRoot)
			}
			t.Setenv("PITOT_TEST_SEEN", seen)
			helperMode := mode
			if mode == "cancel" {
				helperMode = "wait"
			}
			t.Setenv("PITOT_TEST_MODE", helperMode)
			program := agent
			if mode == "missing" {
				program = filepath.Join(seen, "missing-agent")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			canceled := make(chan struct{})
			if mode == "cancel" {
				go func() {
					defer close(canceled)
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
							if _, err := os.Stat(filepath.Join(seen, "ready")); err == nil {
								cancel()
								return
							}
						}
					}
				}()
			}
			var stdout, stderr lockedBuffer
			started := time.Now()
			err := runDev(ctx, []string{"--host", "qwen", "--", program}, &stdout, &stderr)
			elapsed := time.Since(started)
			if mode == "cancel" {
				<-canceled
			}
			if mode == "record" {
				if err != nil {
					t.Fatalf("normal exit: %v", err)
				}
			} else {
				if err == nil || strings.Contains(stdout.String(), "Agent finished.") {
					t.Fatalf("false success: %v\n%s", err, stdout.String())
				}
			}
			if mode == "pipe" {
				if !errors.Is(err, exec.ErrWaitDelay) {
					t.Fatalf("expected incomplete output: %v", err)
				}
				if elapsed >= 4*time.Second {
					t.Errorf("output drain was not bounded: %s", elapsed)
				}
				// The helper descendant self-exits; WaitDelay does not claim to kill it.
				time.Sleep(4*time.Second - elapsed + 100*time.Millisecond)
			}
			entries, readErr := os.ReadDir(tempRoot)
			if readErr != nil || len(entries) != 0 {
				t.Errorf("runtime cleanup: entries=%v err=%v", entries, readErr)
			}
		})
	}
}
