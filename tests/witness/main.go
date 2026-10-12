// Command pitot-witness is a transparent executable wrapper used only by the
// real-agent E2E. It proves that the host invoked Pitot without changing the
// bytes or exit status observed by either process.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type event struct {
	Type string `json:"type"`
	Host struct {
		Name string `json:"name"`
	} `json:"host"`
	Action *struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	} `json:"action"`
	Content *struct {
		Mode string          `json:"mode"`
		Full json.RawMessage `json:"full"`
	} `json:"content"`
}

func main() {
	flags := flag.NewFlagSet("pitot-witness", flag.ContinueOnError)
	pitotFlag := flags.String("real-bin", "", "real Pitot executable")
	receiptFlag := flags.String("receipt", "", "witness receipt path")
	nonceFlag := flags.String("nonce", "", "session nonce")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(125)
	}
	args := flags.Args()
	pitot, receipt, nonce := *pitotFlag, *receiptFlag, *nonceFlag
	if pitot == "" {
		pitot = os.Getenv("PITOT_REAL_BIN")
	}
	if receipt == "" {
		receipt = os.Getenv("PITOT_WITNESS_RECEIPT")
	}
	if nonce == "" {
		nonce = os.Getenv("PITOT_E2E_NONCE")
	}
	if pitot == "" || receipt == "" || nonce == "" {
		fmt.Fprintln(os.Stderr, "pitot-witness: PITOT_REAL_BIN, PITOT_WITNESS_RECEIPT, and PITOT_E2E_NONCE are required")
		os.Exit(125)
	}
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		panic(err)
	}
	command := exec.Command(pitot, args...)
	command.Stdin = bytes.NewReader(in)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	_, _ = os.Stdout.Write(stdout.Bytes())
	_, _ = os.Stderr.Write(stderr.Bytes())
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			code = 125
		}
	}

	host := ""
	if len(args) >= 2 && args[0] == "hook" {
		host = args[1]
	}
	var observed event
	valid := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &observed) == nil &&
		observed.Type == "action.requested" && observed.Host.Name == host &&
		observed.Action != nil && observed.Action.Kind == "shell" &&
		observed.Content != nil && observed.Content.Mode == "full" &&
		strings.Contains(string(observed.Content.Full), nonce)
	actionID := ""
	var nativeSession, nativeCall, nativeRequest string
	if observed.Action != nil {
		actionID = observed.Action.ID
	}
	if host == "deepseek" {
		var request struct {
			Event string `json:"hook_event_name"`
			Tool  string `json:"tool_name"`
			Input struct {
				Command string `json:"command"`
			} `json:"tool_input"`
			RequestID string `json:"request_id"`
			SessionID string `json:"session_id"`
			CallID    string `json:"tool_call_id"`
		}
		var response struct {
			Version   int    `json:"version"`
			RequestID string `json:"request_id"`
			SessionID string `json:"session_id"`
			CallID    string `json:"tool_call_id"`
			ActionID  string `json:"action_id"`
			Outcome   string `json:"outcome"`
		}
		valid = json.Unmarshal(in, &request) == nil && json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &response) == nil &&
			request.Event == "tools/pre-execute" && (request.Tool == "bash" || request.Tool == "pwsh") && strings.Contains(request.Input.Command, nonce) &&
			request.RequestID != "" && request.SessionID != "" && request.CallID != "" &&
			response.Version == 1 && response.RequestID == request.RequestID && response.SessionID == request.SessionID && response.CallID == request.CallID &&
			strings.HasPrefix(response.ActionID, "act_") && ((code == 0 && response.Outcome == "allow") || (code == 2 && response.Outcome == "deny"))
		actionID = response.ActionID
		nativeSession, nativeCall, nativeRequest = request.SessionID, request.CallID, request.RequestID
	}
	record := map[string]any{
		"schema_version": 1, "host": host, "nonce": nonce, "action_kind": "shell",
		"action_id": actionID, "pitot_exit": code, "valid": valid,
	}
	if host == "deepseek" {
		record["session_id"], record["tool_call_id"], record["request_id"] = nativeSession, nativeCall, nativeRequest
	}
	encoded, _ := json.Marshal(record)
	if valid {
		_ = os.MkdirAll(filepath.Dir(receipt), 0o755)
		file, openErr := os.OpenFile(receipt, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if openErr == nil {
			_, _ = file.Write(append(encoded, '\n'))
			_ = file.Close()
		}
	}
	os.Exit(code)
}
