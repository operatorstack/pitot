package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/operatorstack/pitot/internal/devinacp"
)

func TestRunACPParsesSinglePromptOptions(t *testing.T) {
	original := runDevinACP
	t.Cleanup(func() { runDevinACP = original })
	var captured devinacp.Options
	runDevinACP = func(_ context.Context, options devinacp.Options) error {
		captured = options
		return nil
	}
	var stdout, stderr bytes.Buffer
	err := runACP(context.Background(), []string{
		"devin", "--runtime", "/tmp/runtime.json", "--prompt", "fix it",
		"--exec", "/opt/devin", "--model", "opus", "--agent-type", "review", "--cwd", "/tmp/project",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if captured.Runtime != "/tmp/runtime.json" || captured.Prompt != "fix it" || captured.Executable != "/opt/devin" ||
		captured.Model != "opus" || captured.AgentType != "review" || captured.Cwd != "/tmp/project" {
		t.Fatalf("captured options = %+v", captured)
	}
}

func TestRunACPRejectsUnsupportedAndMissingArguments(t *testing.T) {
	tests := [][]string{
		{},
		{"claude"},
		{"devin", "--runtime", "r"},
		{"devin", "--runtime"},
		{"devin", "--runtime", "r", "--prompt", "p", "--resume", "x"},
	}
	for _, args := range tests {
		if err := runACP(context.Background(), args, io.Discard, io.Discard); err == nil {
			t.Errorf("runACP(%v) succeeded", args)
		}
	}
}

func TestDevinOptionsFromAgent(t *testing.T) {
	options, err := devinOptionsFromAgent("/opt/devin", []string{"--model", "opus", "-p", "fix it"}, "runtime.json", io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if options.Executable != "/opt/devin" || options.Model != "opus" || options.Prompt != "fix it" || options.Runtime != "runtime.json" {
		t.Fatalf("options = %+v", options)
	}
	if _, err := devinOptionsFromAgent("claude", []string{"-p", "x"}, "r", io.Discard, io.Discard); err == nil {
		t.Fatal("non-Devin executable accepted")
	}
	if _, err := devinOptionsFromAgent("devin", []string{"--resume", "x"}, "r", io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "unsupported Devin argument") {
		t.Fatalf("unsupported flag error = %v", err)
	}
}

func TestDevTranslatesDevinPrintInvocationToACP(t *testing.T) {
	original := runDevinACP
	t.Cleanup(func() { runDevinACP = original })
	var captured devinacp.Options
	runDevinACP = func(_ context.Context, options devinacp.Options) error {
		captured = options
		return nil
	}
	project := t.TempDir()
	role := buildTestRole(t)
	writeFragment(t, project, "consumer", fmt.Sprintf(`consumers:
  - id: test-consumer
    command: [%q, "--role", "consumer"]
    events: ["action.requested"]
    projection: {content: omit}
`, role))
	t.Setenv("PITOT_RUNTIME", "")
	t.Chdir(project)
	var stdout, stderr lockedBuffer
	if err := runDev(context.Background(), []string{
		"--host", "devin", "--", "devin", "--model", "swe-1.6", "-p", "fix it",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("pitot dev: %v\n%s", err, stderr.String())
	}
	if captured.Executable != "devin" || captured.Prompt != "fix it" || captured.Model != "swe-1.6" ||
		!strings.Contains(captured.Runtime, "pitot-dev-") {
		t.Fatalf("ACP options = %+v", captured)
	}
}

func TestDevinInitAndHookGuidance(t *testing.T) {
	t.Chdir(t.TempDir())
	out, err := runInitHost(t, "devin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "stateful ACP boundary") || !strings.Contains(out, "pitot dev --host devin") {
		t.Fatalf("guidance = %s", out)
	}
	var stdout, stderr bytes.Buffer
	err = runHook(context.Background(), []string{"devin"}, strings.NewReader(`{}`), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "uses ACP") {
		t.Fatalf("hook error = %v", err)
	}
}

func TestDoctorDevinChecksPinnedACPBinary(t *testing.T) {
	originalLookPath, originalCommand := doctorLookPath, doctorCommand
	t.Cleanup(func() {
		doctorLookPath, doctorCommand = originalLookPath, originalCommand
	})
	doctorLookPath = func(string) (string, error) { return os.Args[0], nil }
	doctorCommand = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		scenario := strings.Join(args, " ")
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=TestDoctorDevinHelperProcess", "--")
		command.Env = append(os.Environ(), "PITOT_DOCTOR_DEVIN_HELPER="+scenario)
		return command
	}
	var stdout, stderr bytes.Buffer
	if err := doctorHost("devin", &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if output := stdout.String(); !strings.Contains(output, supportedDevinVersion) || !strings.Contains(output, "ACP v1 boundary: FOUND") {
		t.Fatalf("doctor output = %s", output)
	}
}

func TestDoctorDevinRejectsUnsupportedVersion(t *testing.T) {
	originalLookPath, originalCommand := doctorLookPath, doctorCommand
	t.Cleanup(func() {
		doctorLookPath, doctorCommand = originalLookPath, originalCommand
	})
	doctorLookPath = func(string) (string, error) { return os.Args[0], nil }
	doctorCommand = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=TestDoctorDevinHelperProcess", "--")
		command.Env = append(os.Environ(), "PITOT_DOCTOR_DEVIN_HELPER=old "+strings.Join(args, " "))
		return command
	}
	if err := doctorHost("devin", io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("doctor error = %v", err)
	}
}

func TestDoctorDevinHelperProcess(t *testing.T) {
	scenario := os.Getenv("PITOT_DOCTOR_DEVIN_HELPER")
	if scenario == "" {
		return
	}
	if version, ok := os.LookupEnv("PITOT_DOCTOR_DEVIN_VERSION"); ok && strings.Contains(scenario, "--version") {
		_, _ = io.WriteString(os.Stdout, version)
		os.Exit(0)
	}
	switch {
	case strings.Contains(scenario, "--version") && strings.HasPrefix(scenario, "old "):
		_, _ = io.WriteString(os.Stdout, "devin 2999.0.0 (old)\n")
	case strings.Contains(scenario, "--version"):
		_, _ = io.WriteString(os.Stdout, "devin "+supportedDevinVersion+" (9c803229faa4)\n")
	case strings.Contains(scenario, "acp --help"):
		_, _ = io.WriteString(os.Stdout, "Run an Agent Client Protocol server\n")
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

func TestDoctorDevinVersionMatchesCatalog(t *testing.T) {
	data, err := os.ReadFile("../../adapter-verification.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Agents []struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	for _, agent := range catalog.Agents {
		if agent.ID == "devin" {
			if agent.Version != supportedDevinVersion {
				t.Fatalf("doctor version %s differs from catalog %s", supportedDevinVersion, agent.Version)
			}
			return
		}
	}
	t.Fatal("Devin missing from catalog")
}

func TestDoctorDevinRejectsMalformedBanners(t *testing.T) {
	originalLookPath, originalCommand := doctorLookPath, doctorCommand
	t.Cleanup(func() { doctorLookPath, doctorCommand = originalLookPath, originalCommand })
	doctorLookPath = func(string) (string, error) { return os.Args[0], nil }
	for _, banner := range []string{
		"prefix devin " + supportedDevinVersion + " (9c803229faa4)",
		"devin " + supportedDevinVersion + " (9c803229faa4) suffix",
		"devin " + supportedDevinVersion + " (test)",
		"devin " + supportedDevinVersion + " (ABCDEF12)",
		"devin " + supportedDevinVersion + " (abcdef1)",
		"devin " + supportedDevinVersion + " (9c803229faa4)\nextra",
		"devin " + supportedDevinVersion + "0 (9c803229faa4)",
	} {
		t.Run(banner, func(t *testing.T) {
			doctorCommand = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
				command := exec.CommandContext(ctx, os.Args[0], "-test.run=TestDoctorDevinHelperProcess", "--")
				command.Env = append(os.Environ(), "PITOT_DOCTOR_DEVIN_HELPER="+strings.Join(args, " "), "PITOT_DOCTOR_DEVIN_VERSION="+banner)
				return command
			}
			if err := doctorHost("devin", io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "unsupported") {
				t.Fatalf("doctor error = %v", err)
			}
		})
	}
}
