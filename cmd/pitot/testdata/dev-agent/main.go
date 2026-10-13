// A native, deterministic stand-in used by the dev lifecycle tests.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	mode := os.Getenv("PITOT_TEST_MODE")
	if len(os.Args) > 1 && os.Args[1] == "--hold-output" {
		time.Sleep(4 * time.Second)
		return nil
	}
	seen := os.Getenv("PITOT_TEST_SEEN")
	args := os.Args[1:]
	// When impersonating node.exe, the first argument is its JS entrypoint.
	if os.Getenv("PITOT_TEST_NODE") == "1" {
		args = args[1:]
	}
	data, err := json.Marshal(args)
	if err != nil {
		return err
	}
	for name, value := range map[string]string{"runtime.txt": os.Getenv("PITOT_RUNTIME"), "argc.txt": fmt.Sprint(len(args)), "args.json": string(data), "ready": "yes"} {
		if err := os.WriteFile(filepath.Join(seen, name), []byte(value), 0600); err != nil {
			return err
		}
	}
	switch mode {
	case "fail":
		return fmt.Errorf("agent rejected request")
	case "wait":
		time.Sleep(15 * time.Second)
		return nil
	case "pipe":
		self, err := os.Executable()
		if err != nil {
			return err
		}
		child := exec.Command(self, "--hold-output")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			return err
		}
		return child.Process.Release()
	case "record":
		return nil
	}
	for _, command := range []string{"echo ok", "PITOT_DENY_ME=1 echo no"} {
		payload := fmt.Sprintf(`{"hook_event_name":"PreToolUse","session_id":"dev","tool_name":"Bash","tool_input":{"command":%q}}`, command)
		cmd := exec.Command(os.Getenv("PITOT_BIN"), "hook", "qwen", "--runtime", os.Getenv("PITOT_RUNTIME"))
		cmd.Stdin = strings.NewReader(payload)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if command == "echo ok" && err != nil {
			return err
		}
		if command != "echo ok" && err == nil {
			return fmt.Errorf("denied action unexpectedly succeeded")
		}
	}
	return nil
}
