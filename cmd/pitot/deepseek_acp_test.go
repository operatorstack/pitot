package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDeepSeekExplicitNodeEntrypoint(t *testing.T) {
	root := t.TempDir()
	name := "node"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	node := filepath.Join(root, name)
	if err := os.WriteFile(node, []byte("owned runtime placeholder"), 0o700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(root, "package with spaces", "工具.js")
	program, arguments, err := deepSeekCommand(node, []string{entry})
	if err != nil || program != node || len(arguments) != 1 || arguments[0] != entry {
		t.Fatalf("command = %q %q, error = %v", program, arguments, err)
	}
	for _, args := range [][]string{{"relative.js"}, {filepath.Join(root, "entry.txt")}, {entry, "--extra"}} {
		if _, _, err := deepSeekCommand(node, args); err == nil {
			t.Fatalf("accepted unsupported entrypoint arguments %q", args)
		}
	}
	if _, _, err := deepSeekCommand(filepath.Join(root, "dsh"), []string{entry}); err == nil {
		t.Fatal("accepted a JS entrypoint with a non-Node executable")
	}
}

func TestDeepSeekRejectsDuplicateEntrypointBeforeLaunch(t *testing.T) {
	err := runDeepSeekACP(context.Background(), []string{"--runtime", "r", "--prompt", "p", "--entrypoint", "first.js", "--entrypoint", "second.js"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "duplicate DeepSeek entrypoint") {
		t.Fatalf("duplicate entrypoint error = %v", err)
	}
}
