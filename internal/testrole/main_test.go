package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanaryTerminalGuardRejectsPipeBeforeReceipt(t *testing.T) {
	if os.Getenv("PITOT_TEST_CANARY_CHILD") == "1" {
		os.Args = []string{"testrole", "--role", "canary", "--receipt", os.Getenv("PITOT_TEST_RECEIPT"), "--require-terminal", "PITOT_ALLOW", "nonce"}
		main()
		return
	}
	path := filepath.Join(t.TempDir(), "receipt")
	command := exec.Command(os.Args[0], "-test.run=^TestCanaryTerminalGuardRejectsPipeBeforeReceipt$")
	command.Env = append(os.Environ(), "PITOT_TEST_CANARY_CHILD=1", "PITOT_TEST_RECEIPT="+path)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "canary requires terminal output") {
		t.Fatalf("expected terminal rejection, got %s, %v", output, err)
	}
	if strings.Contains(string(output), "PITOT_CANARY_RESULT") {
		t.Fatal("rejected canary emitted a result")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("rejected canary created a receipt: %v", err)
	}
}

func TestCanaryWritesNonceBoundReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canary.jsonl")
	if err := canary(path, []string{"PITOT_ALLOW", "nonce"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "PITOT_ALLOW nonce\n" {
		t.Fatalf("receipt = %q", raw)
	}
}

func TestCanaryRejectsIncompleteInvocation(t *testing.T) {
	if err := canary("", []string{"PITOT_ALLOW"}); err == nil {
		t.Fatal("expected incomplete canary invocation to fail")
	}
}
