package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
