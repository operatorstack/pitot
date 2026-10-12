package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeepSeekHookNeverTreatsObservationAsPermission(t *testing.T) {
	t.Setenv("PITOT_RUNTIME", "")
	for _, payload := range []string{`{}`, `{"hook_event_name":"tools/pre-execute","tool_name":"bash","tool_input":{"command":"echo ok"},"request_id":"r","session_id":"s","tool_call_id":"t"}`} {
		var output bytes.Buffer
		err := runHook(context.Background(), []string{"deepseek"}, strings.NewReader(payload), &output, io.Discard)
		var response deepSeekDecision
		if !errors.Is(err, errBlocked) || json.Unmarshal(output.Bytes(), &response) != nil || response.Outcome != "unavailable" {
			t.Fatalf("err=%v output=%s", err, output.String())
		}
	}
}

func TestDeepSeekHookRejectsMalformedNativeRequests(t *testing.T) {
	for _, payload := range []string{
		`{"request_id":"one","request_id":"two"}`,
		`{"Hook_Event_Name":"tools/pre-execute"}`,
		`{"hook_event_name":"tools/pre-execute","tool_name":"read_file","tool_input":{"command":"echo ok"},"request_id":"r","session_id":"s","tool_call_id":"t"}`,
		`{"hook_event_name":"tools/pre-execute","tool_name":"bash","tool_input":{"command":" "},"request_id":"r","session_id":"s","tool_call_id":"t"}`,
	} {
		var output bytes.Buffer
		err := runHook(context.Background(), []string{"deepseek", "--runtime", filepath.Join(t.TempDir(), "absent.json")}, strings.NewReader(payload), &output, io.Discard)
		if !errors.Is(err, errBlocked) || strings.Contains(output.String(), `"outcome":"allow"`) {
			t.Fatalf("err=%v output=%s", err, output.String())
		}
	}
}

func TestDeepSeekOwnedIntegrationDetectsDrift(t *testing.T) {
	root := t.TempDir()
	if err := writeDeepSeekIntegration(root, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := checkDeepSeekIntegration(root); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(root, filepath.FromSlash(deepSeekPluginPath))
	if err := os.WriteFile(plugin, []byte("foreign edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeDeepSeekIntegration(root, false, io.Discard); err == nil {
		t.Fatal("silently replaced drift")
	}
	if data, _ := os.ReadFile(plugin); string(data) != "foreign edit" {
		t.Fatal("modified drifted file")
	}
	if err := writeDeepSeekIntegration(root, true, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := checkDeepSeekIntegration(root); err != nil {
		t.Fatal(err)
	}
}
