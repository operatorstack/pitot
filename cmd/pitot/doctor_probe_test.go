package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func useDoctorProbeFixture(t *testing.T, scenario string) {
	t.Helper()
	originalCommand, originalLookup := doctorCommand, doctorLookPath
	originalTimeout, originalWait := doctorProbeTimeout, doctorProbeWaitDelay
	t.Cleanup(func() {
		doctorCommand, doctorLookPath = originalCommand, originalLookup
		doctorProbeTimeout, doctorProbeWaitDelay = originalTimeout, originalWait
	})
	doctorProbeTimeout, doctorProbeWaitDelay = 2*time.Second, 200*time.Millisecond
	doctorLookPath = func(string) (string, error) { return os.Args[0], nil }
	doctorCommand = func(ctx context.Context, _ string, arguments ...string) *exec.Cmd {
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDoctorProbeHelperProcess$", "--")
		command.Env = append(os.Environ(), "PITOT_DOCTOR_PROBE="+scenario, "PITOT_DOCTOR_PROBE_ARGS="+strings.Join(arguments, " "))
		return command
	}
}

func TestDoctorProbeDeadlineRejectsPartialSuccess(t *testing.T) {
	for _, scenario := range []string{"version-timeout", "help-timeout"} {
		t.Run(scenario, func(t *testing.T) {
			useDoctorProbeFixture(t, scenario)
			var stdout, stderr bytes.Buffer
			start := time.Now()
			err := doctorDevin(&stdout, &stderr)
			if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out") {
				t.Fatalf("expected deadline failure, got %v", err)
			}
			if time.Since(start) > 5*time.Second || strings.Contains(stdout.String(), "ready:") {
				t.Fatalf("probe failed to return promptly without readiness: %s", stdout.String())
			}
			if !strings.Contains(stderr.String(), "devin") {
				t.Fatalf("partial diagnostic lost: %s", stderr.String())
			}
		})
	}
}

func TestDoctorProbeRejectsInheritedOutputPipe(t *testing.T) {
	useDoctorProbeFixture(t, "inherited-pipe")
	start := time.Now()
	out, err := doctorProbe("fixture", "doctor")
	if !errors.Is(err, exec.ErrWaitDelay) || !strings.Contains(string(out), "configuration valid") {
		t.Fatalf("output=%q error=%v", out, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("inherited output pipe blocked probe return")
	}
}

func TestDoctorProbeRejectsOversizedSuccess(t *testing.T) {
	useDoctorProbeFixture(t, "overflow")
	out, err := doctorProbe("fixture", "acp", "--help")
	if err == nil || !strings.Contains(err.Error(), "exceeded 64 KiB") || len(out) != doctorOutputLimit {
		t.Fatalf("retained bytes=%d error=%v", len(out), err)
	}
	if !strings.HasPrefix(string(out), "Run an Agent Client Protocol server") {
		t.Fatalf("diagnostic prefix was not retained: %q", out[:80])
	}
}

func TestDoctorDevinPreservesFailedMetadataDiagnostics(t *testing.T) {
	for _, scenario := range []string{"version-failure", "help-failure"} {
		t.Run(scenario, func(t *testing.T) {
			useDoctorProbeFixture(t, scenario)
			var stdout, stderr bytes.Buffer
			err := doctorDevin(&stdout, &stderr)
			if err == nil || !strings.Contains(stderr.String(), "fixture diagnostic sentinel") || strings.Contains(stdout.String(), "ready:") {
				t.Fatalf("error=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
		})
	}
}

func TestDoctorKimiRejectsIncompleteProbe(t *testing.T) {
	for _, scenario := range []string{"doctor-timeout", "inherited-pipe", "overflow", "doctor-failure", "nominal"} {
		t.Run(scenario, func(t *testing.T) {
			useDoctorProbeFixture(t, scenario)
			directory := t.TempDir()
			t.Setenv("KIMI_CODE_HOME", directory)
			if err := os.WriteFile(filepath.Join(directory, "config.toml"), []byte(kimiHookConfig), 0o600); err != nil {
				t.Fatal(err)
			}
			// Kimi's actual PATH lookup is kept: provide an owned executable marker.
			name := "kimi"
			if os.PathSeparator == '\\' {
				name += ".exe"
			}
			if err := os.WriteFile(filepath.Join(directory, name), []byte("fixture"), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			var stdout, stderr bytes.Buffer
			err := doctorHost("kimi", &stdout, &stderr)
			if scenario == "nominal" {
				if err != nil || !strings.Contains(stdout.String(), "ready:") {
					t.Fatalf("nominal error=%v stdout=%q", err, stdout.String())
				}
			} else if err == nil || strings.Contains(stdout.String(), "ready:") || !strings.Contains(stdout.String(), "config parses: FAIL") {
				t.Fatalf("incomplete probe error=%v stdout=%q", err, stdout.String())
			}
		})
	}
}

func TestDoctorProbeHelperProcess(t *testing.T) {
	scenario := os.Getenv("PITOT_DOCTOR_PROBE")
	if scenario == "" {
		return
	}
	arguments := os.Getenv("PITOT_DOCTOR_PROBE_ARGS")
	if scenario == "pipe-child" {
		time.Sleep(3 * time.Second)
		os.Exit(0)
	}
	version := arguments == "--version"
	if version {
		fmt.Printf("devin %s (9c803229faa4)\n", supportedDevinVersion)
	} else if arguments == "acp --help" {
		fmt.Println("Run an Agent Client Protocol server")
	} else {
		fmt.Println("configuration valid")
	}
	if scenario == "overflow" {
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", doctorOutputLimit+1))
	}
	if scenario == "version-failure" && version || scenario == "help-failure" && !version || scenario == "doctor-failure" {
		fmt.Fprintln(os.Stderr, "fixture diagnostic sentinel")
		os.Exit(7)
	}
	if scenario == "version-timeout" && version || scenario == "help-timeout" && !version || scenario == "doctor-timeout" {
		time.Sleep(4 * time.Second)
	}
	if scenario == "inherited-pipe" {
		child := exec.Command(os.Args[0], "-test.run=^TestDoctorProbeHelperProcess$", "--")
		child.Env = append(os.Environ(), "PITOT_DOCTOR_PROBE=pipe-child")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	os.Exit(0)
}
