package devinacp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/operatorstack/pitot/schema"
)

func TestRunAllowDenyAndContinue(t *testing.T) {
	var mu sync.Mutex
	var events []schema.Event
	var calls int
	deliver := func(_ context.Context, event schema.Event) (*schema.ControlResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
		calls++
		outcome := schema.OutcomeAllow
		if calls == 2 {
			outcome = schema.OutcomeDeny
		}
		return &schema.ControlResponse{ActionID: event.Action.ID, Outcome: outcome}, nil
	}
	var output strings.Builder
	err := Run(context.Background(), helperOptions(t, "allow-deny", deliver, &output))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if output.String() != "PITOT_ACP_COMPLETE" {
		t.Fatalf("output = %q", output.String())
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	for index, event := range events {
		if event.Host.Name != "devin" || event.Action == nil || !strings.HasPrefix(event.Action.ID, "act_") {
			t.Errorf("event %d = %+v", index, event)
		}
		if event.SessionID != "session-1" || event.Observation.Source != schema.SourceHostEvent {
			t.Errorf("event %d boundary metadata = %+v", index, event)
		}
	}
	if string(events[0].Content.Full) != `"echo allow"` || string(events[1].Content.Full) != `"echo deny"` {
		t.Errorf("commands = %s, %s", events[0].Content.Full, events[1].Content.Full)
	}
}

func TestRunRejectsUnsupportedPermissionWithoutDelivery(t *testing.T) {
	delivered := false
	options := helperOptions(t, "unsupported", func(context.Context, schema.Event) (*schema.ControlResponse, error) {
		delivered = true
		return nil, nil
	}, io.Discard)
	if err := Run(context.Background(), options); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if delivered {
		t.Fatal("unsupported permission reached Pitot runtime")
	}
}

func TestRunFailsClosedWhenControllerFaults(t *testing.T) {
	options := helperOptions(t, "controller-fault", func(context.Context, schema.Event) (*schema.ControlResponse, error) {
		return nil, errors.New("unavailable")
	}, io.Discard)
	if err := Run(context.Background(), options); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRunAllowsOnceWithoutController(t *testing.T) {
	options := helperOptions(t, "no-controller", func(context.Context, schema.Event) (*schema.ControlResponse, error) {
		return nil, nil
	}, io.Discard)
	if err := Run(context.Background(), options); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRunRejectsMissingToolCorrelationWithoutDelivery(t *testing.T) {
	delivered := false
	options := helperOptions(t, "missing-correlation", func(context.Context, schema.Event) (*schema.ControlResponse, error) {
		delivered = true
		return nil, nil
	}, io.Discard)
	if err := Run(context.Background(), options); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if delivered {
		t.Fatal("uncorrelated permission reached Pitot runtime")
	}
}

func TestRunRejectsMissingRejectOption(t *testing.T) {
	options := helperOptions(t, "missing-reject", nil, io.Discard)
	if err := Run(context.Background(), options); err == nil || !strings.Contains(err.Error(), "omitted reject_once") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunRejectsProtocolDrift(t *testing.T) {
	options := helperOptions(t, "protocol-drift", nil, io.Discard)
	if err := Run(context.Background(), options); err == nil || !strings.Contains(err.Error(), "protocol version 2") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunRejectsMalformedJSONRPC(t *testing.T) {
	options := helperOptions(t, "malformed", nil, io.Discard)
	if err := Run(context.Background(), options); err == nil || !strings.Contains(err.Error(), "malformed JSON-RPC") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunRejectsWrongJSONRPCVersion(t *testing.T) {
	options := helperOptions(t, "wrong-jsonrpc", nil, io.Discard)
	if err := Run(context.Background(), options); err == nil || !strings.Contains(err.Error(), "JSON-RPC version") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunRejectsMalformedPermissionBeforeStopping(t *testing.T) {
	options := helperOptions(t, "malformed-permission", nil, io.Discard)
	if err := Run(context.Background(), options); err == nil || !strings.Contains(err.Error(), "malformed permission") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunReportsChildFailure(t *testing.T) {
	options := helperOptions(t, "child-failure", nil, io.Discard)
	if err := Run(context.Background(), options); err == nil {
		t.Fatal("expected child failure")
	}
}

func TestRunCancellationStopsChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	options := helperOptions(t, "block", nil, io.Discard)
	if err := Run(ctx, options); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func helperOptions(t *testing.T, scenario string, deliver DeliverFunc, output io.Writer) Options {
	t.Helper()
	if deliver == nil {
		deliver = func(context.Context, schema.Event) (*schema.ControlResponse, error) { return nil, nil }
	}
	return Options{
		Executable: os.Args[0],
		Prompt:     "test prompt",
		Cwd:        t.TempDir(),
		Stdout:     output,
		Stderr:     io.Discard,
		Deliver:    deliver,
		command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=TestACPHelperProcess", "--")
			command.Env = append(os.Environ(), "PITOT_ACP_HELPER="+scenario)
			return command
		},
	}
}

func TestACPHelperProcess(t *testing.T) {
	scenario := os.Getenv("PITOT_ACP_HELPER")
	if scenario == "" {
		return
	}
	if scenario == "child-failure" {
		os.Exit(17)
	}
	reader := bufio.NewScanner(os.Stdin)
	writer := json.NewEncoder(os.Stdout)
	readRequest := func() map[string]any {
		if !reader.Scan() {
			os.Exit(20)
		}
		var value map[string]any
		if json.Unmarshal(reader.Bytes(), &value) != nil {
			os.Exit(21)
		}
		return value
	}
	respond := func(request map[string]any, result any) {
		_ = writer.Encode(map[string]any{"jsonrpc": "2.0", "id": request["id"], "result": result})
	}

	initialize := readRequest()
	if scenario == "malformed" {
		fmt.Fprintln(os.Stdout, "{broken")
		return
	}
	if scenario == "wrong-jsonrpc" {
		_ = writer.Encode(map[string]any{"jsonrpc": "1.0", "id": initialize["id"], "result": map[string]any{"protocolVersion": 1}})
		return
	}
	version := 1
	if scenario == "protocol-drift" {
		version = 2
	}
	respond(initialize, map[string]any{"protocolVersion": version})
	if version != 1 {
		return
	}
	newSession := readRequest()
	respond(newSession, map[string]any{"sessionId": "session-1"})
	prompt := readRequest()
	if scenario == "block" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}

	switch scenario {
	case "allow-deny":
		emitTool(writer, "allow", "exec", "echo allow")
		emitPermission(writer, 101, "allow", true)
		expectPermission(reader, "allow")
		emitTool(writer, "deny", "exec", "echo deny")
		emitPermission(writer, 102, "deny", true)
		expectPermission(reader, "reject")
		_ = writer.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
			"sessionId": "session-1",
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content":       map[string]any{"type": "text", "text": "PITOT_ACP_COMPLETE"},
			},
		}})
	case "unsupported":
		emitTool(writer, "other", "browser", "")
		emitPermission(writer, 101, "other", true)
		expectPermission(reader, "reject")
	case "controller-fault":
		emitTool(writer, "fault", "exec", "echo fault")
		emitPermission(writer, 101, "fault", true)
		expectPermission(reader, "reject")
	case "no-controller":
		emitTool(writer, "allow", "exec", "echo allow")
		emitPermission(writer, 101, "allow", true)
		expectPermission(reader, "allow")
	case "missing-correlation":
		emitPermission(writer, 101, "missing", true)
		expectPermission(reader, "reject")
	case "malformed-permission":
		_ = writer.Encode(map[string]any{"jsonrpc": "2.0", "id": 101, "method": "session/request_permission", "params": map[string]any{
			"sessionId": []string{"wrong-type"},
			"options": []map[string]string{
				{"optionId": "allow", "kind": "allow_once"},
				{"optionId": "reject", "kind": "reject_once"},
			},
		}})
		expectPermission(reader, "reject")
		return
	case "missing-reject":
		emitTool(writer, "missing", "exec", "echo missing")
		emitPermission(writer, 101, "missing", false)
		expectCancelled(reader)
		return
	}
	respond(prompt, map[string]any{"stopReason": "end_turn"})
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func emitTool(writer *json.Encoder, id, tool, command string) {
	rawInput := map[string]any{}
	if command != "" {
		rawInput["command"] = command
	}
	_ = writer.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
		"sessionId": "session-1",
		"update": map[string]any{
			"sessionUpdate": "tool_call",
			"toolCallId":    id,
			"rawInput":      rawInput,
			"_meta":         map[string]any{"cognition.ai/inferenceToolName": tool},
		},
	}})
}

func emitPermission(writer *json.Encoder, requestID int, toolID string, reject bool) {
	options := []map[string]string{{"optionId": "allow", "kind": "allow_once"}}
	if reject {
		options = append(options, map[string]string{"optionId": "reject", "kind": "reject_once"})
	}
	_ = writer.Encode(map[string]any{"jsonrpc": "2.0", "id": requestID, "method": "session/request_permission", "params": map[string]any{
		"sessionId": "session-1",
		"toolCall":  map[string]string{"toolCallId": toolID},
		"options":   options,
	}})
}

func expectPermission(reader *bufio.Scanner, option string) {
	value := readHelperResponse(reader)
	result := value["result"].(map[string]any)
	outcome := result["outcome"].(map[string]any)
	if outcome["optionId"] != option {
		os.Exit(30)
	}
}

func expectCancelled(reader *bufio.Scanner) {
	value := readHelperResponse(reader)
	result := value["result"].(map[string]any)
	outcome := result["outcome"].(map[string]any)
	if outcome["outcome"] != "cancelled" {
		os.Exit(31)
	}
}

func readHelperResponse(reader *bufio.Scanner) map[string]any {
	if !reader.Scan() {
		os.Exit(32)
	}
	var value map[string]any
	if json.Unmarshal(reader.Bytes(), &value) != nil {
		os.Exit(33)
	}
	return value
}
