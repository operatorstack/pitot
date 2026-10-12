package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/operatorstack/pitot/adapters"
	"github.com/operatorstack/pitot/protocol"
	"github.com/operatorstack/pitot/runtime"
	"github.com/operatorstack/pitot/schema"
	"github.com/operatorstack/pitot/sensor"
)

// deepSeekDecision is a single-request protocol, separate from observation-only
// hook output. Exit zero is reserved for an explicit correlated grant.
type deepSeekDecision struct {
	Version    int    `json:"version"`
	RequestID  string `json:"request_id"`
	SessionID  string `json:"session_id"`
	ToolCallID string `json:"tool_call_id"`
	ActionID   string `json:"action_id"`
	Outcome    string `json:"outcome"`
	Reason     string `json:"reason,omitempty"`
}

func runDeepSeekHook(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	decision := deepSeekDecision{Version: 1, Outcome: "unavailable"}
	finish := func(reason string) error {
		decision.Reason = reason
		if err := json.NewEncoder(stdout).Encode(decision); err != nil {
			return fmt.Errorf("pitot: write DeepSeek decision: %w", err)
		}
		if decision.Outcome == schema.OutcomeAllow {
			return nil
		}
		return errBlocked
	}
	runtimePath, err := parseRuntimeFlag(args, true)
	if err != nil {
		return finish("Pitot runtime is required")
	}
	payload, err := io.ReadAll(io.LimitReader(stdin, (4<<20)+1))
	if err != nil || len(payload) > 4<<20 {
		return finish("Invalid DeepSeek request")
	}
	var raw adapters.RawBoundaryEvent
	value, parseErr := protocol.Parse(payload)
	object, ok := value.(map[string]any)
	if parseErr != nil || !ok {
		return finish("Invalid DeepSeek request")
	}
	for key := range object {
		switch key {
		case "hook_event_name", "tool_name", "tool_input", "session_id", "tool_call_id", "request_id", "root_call_id":
		default:
			return finish("Invalid DeepSeek request field")
		}
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return finish("Invalid DeepSeek request")
	}
	decision.RequestID, decision.SessionID, decision.ToolCallID = raw.RequestID, raw.SessionID, raw.ToolCallID
	event, err := sensor.Decode(adapters.DeepSeek, payload, "full")
	if err != nil {
		return finish("Invalid DeepSeek shell boundary")
	}
	actionID, err := runtime.NewActionID()
	if err != nil {
		return finish("Cannot identify DeepSeek action")
	}
	decision.ActionID = actionID
	event.Action.ID = actionID
	client, err := runtime.OpenClient(runtimePath)
	if err != nil {
		return finish("Pitot runtime is unavailable")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	response, err := client.DeliverControlledEvent(requestCtx, event)
	if err != nil || response == nil {
		return finish("Pitot Controller is unavailable")
	}
	if response.ActionID != actionID || (response.Outcome != schema.OutcomeAllow && response.Outcome != schema.OutcomeDeny) {
		return finish("Invalid Pitot Controller resolution")
	}
	decision.Outcome = response.Outcome
	reason := response.Message
	if len(reason) > 1024 {
		reason = reason[:1024]
	}
	if decision.Outcome == schema.OutcomeDeny && reason == "" {
		reason = "Pitot Controller denied the shell request"
	}
	return finish(reason)
}
