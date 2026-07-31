// Package devinacp implements Pitot's single-turn Devin ACP transport.
package devinacp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/operatorstack/pitot/adapters"
	"github.com/operatorstack/pitot/projection"
	"github.com/operatorstack/pitot/runtime"
	"github.com/operatorstack/pitot/schema"
	"github.com/operatorstack/pitot/sensor"
)

const maxMessageBytes = 4 << 20

// DeliverFunc routes a normalized action through the Pitot runtime.
type DeliverFunc func(context.Context, schema.Event) (*schema.ControlResponse, error)

// Options configures one Devin ACP prompt.
type Options struct {
	Executable string
	Prompt     string
	Cwd        string
	Model      string
	AgentType  string
	Runtime    string
	Stdout     io.Writer
	Stderr     io.Writer
	Deliver    DeliverFunc
	command    func(context.Context, string, ...string) *exec.Cmd
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type client struct {
	ctx      context.Context
	stdin    io.WriteCloser
	scanner  *bufio.Scanner
	stdout   io.Writer
	deliver  DeliverFunc
	commands map[string]string
	nextID   int
	writeMu  sync.Mutex
}

// Run launches Devin as an ACP subprocess and completes one prompt turn.
func Run(ctx context.Context, options Options) error {
	if options.Executable == "" {
		options.Executable = "devin"
	}
	if options.Prompt == "" {
		return errors.New("pitot acp: --prompt is required")
	}
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	if options.Stderr == nil {
		options.Stderr = io.Discard
	}
	if options.Cwd == "" {
		options.Cwd = "."
	}
	deliver := options.Deliver
	if deliver == nil {
		if options.Runtime == "" {
			return errors.New("pitot acp: runtime is required")
		}
		runtimeClient, err := runtime.OpenClient(options.Runtime)
		if err != nil {
			return fmt.Errorf("pitot acp: open runtime: %w", err)
		}
		deliver = runtimeClient.DeliverEvent
	}

	args := []string{"acp"}
	if options.AgentType != "" {
		args = append(args, "--agent-type", options.AgentType)
	}
	if options.Model != "" {
		args = append(args, "--model", options.Model)
	}
	commandBuilder := options.command
	if commandBuilder == nil {
		commandBuilder = exec.CommandContext
	}
	command := commandBuilder(ctx, options.Executable, args...)
	command.Dir = options.Cwd
	stdin, err := command.StdinPipe()
	if err != nil {
		return fmt.Errorf("pitot acp: child stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("pitot acp: child stdout: %w", err)
	}
	command.Stderr = options.Stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("pitot acp: start %s: %w", options.Executable, err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), maxMessageBytes)
	transport := &client{
		ctx:      ctx,
		stdin:    stdin,
		scanner:  scanner,
		stdout:   options.Stdout,
		deliver:  deliver,
		commands: map[string]string{},
	}
	runErr := transport.run(options)
	_ = stdin.Close()
	waitErr := waitForChild(command)
	if runErr != nil {
		return runErr
	}
	if waitErr != nil {
		return fmt.Errorf("pitot acp: Devin exited: %w", waitErr)
	}
	return nil
}

func waitForChild(command *exec.Cmd) error {
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		_ = command.Process.Kill()
		<-done
		return nil
	}
}

func (c *client) run(options Options) error {
	var initialized struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := c.call("initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{},
		"clientInfo": map[string]string{
			"name": "pitot", "title": "Pitot", "version": adapters.AdapterVersion,
		},
	}, &initialized); err != nil {
		return err
	}
	if initialized.ProtocolVersion != 1 {
		return fmt.Errorf("pitot acp: unsupported negotiated protocol version %d", initialized.ProtocolVersion)
	}

	absoluteCwd, err := filepath.Abs(options.Cwd)
	if err != nil {
		return fmt.Errorf("pitot acp: resolve cwd: %w", err)
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.call("session/new", map[string]any{"cwd": absoluteCwd, "mcpServers": []any{}}, &session); err != nil {
		return err
	}
	if session.SessionID == "" {
		return errors.New("pitot acp: Devin returned an empty session id")
	}
	var promptResult struct {
		StopReason string `json:"stopReason"`
	}
	if err := c.call("session/prompt", map[string]any{
		"sessionId": session.SessionID,
		"prompt":    []map[string]string{{"type": "text", "text": options.Prompt}},
	}, &promptResult); err != nil {
		return err
	}
	if promptResult.StopReason == "" {
		return errors.New("pitot acp: Devin prompt completed without a stop reason")
	}
	return nil
}

func (c *client) call(method string, params any, target any) error {
	c.nextID++
	id := c.nextID
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for c.scanner.Scan() {
		var message rpcMessage
		if err := json.Unmarshal(c.scanner.Bytes(), &message); err != nil {
			return fmt.Errorf("pitot acp: malformed JSON-RPC message: %w", err)
		}
		if message.JSONRPC != "2.0" {
			return fmt.Errorf("pitot acp: unsupported JSON-RPC version %q", message.JSONRPC)
		}
		if message.Method != "" {
			if err := c.handleInbound(message); err != nil {
				return err
			}
			continue
		}
		var responseID int
		if len(message.ID) == 0 || json.Unmarshal(message.ID, &responseID) != nil || responseID != id {
			return errors.New("pitot acp: unexpected JSON-RPC response id")
		}
		if message.Error != nil {
			return fmt.Errorf("pitot acp: %s failed (%d): %s", method, message.Error.Code, message.Error.Message)
		}
		if err := json.Unmarshal(message.Result, target); err != nil {
			return fmt.Errorf("pitot acp: decode %s response: %w", method, err)
		}
		return nil
	}
	if err := c.scanner.Err(); err != nil {
		return fmt.Errorf("pitot acp: read Devin output: %w", err)
	}
	return errors.New("pitot acp: Devin closed the protocol stream")
}

func (c *client) handleInbound(message rpcMessage) error {
	switch message.Method {
	case "session/update":
		return c.handleUpdate(message.Params)
	case "session/request_permission":
		if len(message.ID) == 0 {
			return errors.New("pitot acp: permission request omitted its JSON-RPC id")
		}
		return c.handlePermission(message.ID, message.Params)
	default:
		if len(message.ID) == 0 {
			return nil
		}
		return c.write(map[string]any{
			"jsonrpc": "2.0",
			"id":      json.RawMessage(message.ID),
			"error":   rpcError{Code: -32601, Message: "unsupported ACP client method"},
		})
	}
}

func (c *client) handleUpdate(raw json.RawMessage) error {
	var params struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			SessionUpdate string          `json:"sessionUpdate"`
			ToolCallID    string          `json:"toolCallId"`
			RawInput      map[string]any  `json:"rawInput"`
			Content       json.RawMessage `json:"content"`
			Meta          map[string]any  `json:"_meta"`
		} `json:"update"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return fmt.Errorf("pitot acp: malformed session update: %w", err)
	}
	switch params.Update.SessionUpdate {
	case "tool_call":
		command, _ := params.Update.RawInput["command"].(string)
		tool, _ := params.Update.Meta["cognition.ai/inferenceToolName"].(string)
		if params.SessionID != "" && params.Update.ToolCallID != "" && tool == "exec" && command != "" {
			c.commands[commandKey(params.SessionID, params.Update.ToolCallID)] = command
		}
	case "agent_message_chunk":
		var content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(params.Update.Content, &content); err != nil {
			return fmt.Errorf("pitot acp: malformed agent message chunk: %w", err)
		}
		if content.Type == "text" && content.Text != "" {
			if _, err := io.WriteString(c.stdout, content.Text); err != nil {
				return fmt.Errorf("pitot acp: write agent output: %w", err)
			}
		}
	}
	return nil
}

type permissionOption struct {
	OptionID string `json:"optionId"`
	Kind     string `json:"kind"`
}

func (c *client) handlePermission(id json.RawMessage, raw json.RawMessage) error {
	var optionEnvelope struct {
		Options []permissionOption `json:"options"`
	}
	_ = json.Unmarshal(raw, &optionEnvelope)
	var params struct {
		SessionID string `json:"sessionId"`
		ToolCall  struct {
			ToolCallID string `json:"toolCallId"`
		} `json:"toolCall"`
		Options []permissionOption `json:"options"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		if reject := optionFor(optionEnvelope.Options, "reject_once"); reject != "" {
			_ = c.permissionResponse(id, reject, false)
		}
		return fmt.Errorf("pitot acp: malformed permission request: %w", err)
	}
	reject := optionFor(params.Options, "reject_once")
	if reject == "" {
		_ = c.permissionResponse(id, "", true)
		return errors.New("pitot acp: permission request omitted reject_once")
	}
	command := c.commands[commandKey(params.SessionID, params.ToolCall.ToolCallID)]
	if command == "" {
		return c.permissionResponse(id, reject, false)
	}

	payload, _ := json.Marshal(map[string]any{
		"hook_event_name": "session/request_permission",
		"tool_name":       "exec",
		"tool_input":      map[string]string{"command": command},
	})
	event, err := sensor.Decode(adapters.Devin, payload, projection.Full)
	if err != nil {
		return c.permissionResponse(id, reject, false)
	}
	actionID, err := runtime.NewActionID()
	if err != nil {
		return c.permissionResponse(id, reject, false)
	}
	event.Action.ID = actionID
	event.SessionID = params.SessionID
	response, deliverErr := c.deliver(c.ctx, event)
	allow := deliverErr == nil && (response == nil || response.Outcome == schema.OutcomeAllow)
	if response != nil && response.ActionID != actionID {
		allow = false
	}
	if allow {
		if option := optionFor(params.Options, "allow_once"); option != "" {
			delete(c.commands, commandKey(params.SessionID, params.ToolCall.ToolCallID))
			return c.permissionResponse(id, option, false)
		}
	}
	delete(c.commands, commandKey(params.SessionID, params.ToolCall.ToolCallID))
	return c.permissionResponse(id, reject, false)
}

func (c *client) permissionResponse(id json.RawMessage, optionID string, cancelled bool) error {
	outcome := map[string]any{"outcome": "cancelled"}
	if !cancelled {
		outcome = map[string]any{"outcome": "selected", "optionId": optionID}
	}
	return c.write(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result":  map[string]any{"outcome": outcome},
	})
}

func optionFor(options []permissionOption, kind string) string {
	for _, option := range options {
		if option.Kind == kind {
			return option.OptionID
		}
	}
	return ""
}

func commandKey(sessionID, toolCallID string) string { return sessionID + "\x00" + toolCallID }

func (c *client) write(value any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if _, err := c.stdin.Write(encoded); err != nil {
		return fmt.Errorf("pitot acp: write Devin input: %w", err)
	}
	return nil
}
