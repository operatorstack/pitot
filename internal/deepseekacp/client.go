// Package deepseekacp drives one DeepSeek Harness prompt while its native
// pre-execute plugin owns shell control.
package deepseekacp

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	deepseek "github.com/operatorstack/pitot/integrations/deepseek"
	"github.com/operatorstack/pitot/protocol"
)

type Options struct {
	Executable string
	Arguments  []string
	Pitot      string
	Runtime    string
	Cwd        string
	Prompt     string
	BaseURL    string
	Model      string
	APIKeyEnv  string
	SessionDir string
	Stdout     io.Writer
	Stderr     io.Writer
}

func quoted(value string) string { b, _ := json.Marshal(value); return string(b) }

// Run never sends a prompt until the owned plugin has registered its native
// gate and the complete application has signalled readiness.
func Run(ctx context.Context, options Options) error {
	if options.Prompt == "" || options.Runtime == "" || options.Pitot == "" {
		return errors.New("pitot: DeepSeek requires prompt, runtime and Pitot executable")
	}
	if options.Executable == "" {
		options.Executable = "dsh"
	}
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	if options.Stderr == nil {
		options.Stderr = io.Discard
	}
	cwd, err := filepath.Abs(options.Cwd)
	if err != nil {
		return err
	}
	runtimePath, err := filepath.Abs(options.Runtime)
	if err != nil {
		return err
	}
	pitot, err := filepath.Abs(options.Pitot)
	if err != nil {
		return err
	}
	root := options.SessionDir
	if root == "" {
		root, err = os.MkdirTemp("", "pitot-deepseek-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(root)
	} else {
		if !filepath.IsAbs(root) {
			return errors.New("pitot: DeepSeek session directory must be absolute")
		}
		if err := os.Mkdir(root, 0o700); err != nil {
			return fmt.Errorf("pitot: create new DeepSeek session directory: %w", err)
		}
	}
	for _, name := range []string{"home", "tmp", "cache"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			return err
		}
	}
	plugin := filepath.Join(root, "pitot.mjs")
	if err := os.WriteFile(plugin, []byte(deepseek.Plugin), 0o600); err != nil {
		return err
	}
	nonceBytes := make([]byte, 24)
	if _, err := rand.Read(nonceBytes); err != nil {
		return err
	}
	nonce := hex.EncodeToString(nonceBytes)
	patch := "- id: acp\n  inject: [acpAppStartup, pitotShellControl]\n- id: session-telemetry-otel\n  disabled: true\n- insert:\n    - id: pitot-shell-control\n      name: " + quoted(plugin) + "\n      config:\n        executable: " + quoted(pitot) + "\n        runtime: " + quoted(runtimePath) + "\n        nonce: " + quoted(nonce) + "\n"
	if options.BaseURL != "" {
		if options.Model == "" || options.APIKeyEnv == "" {
			return errors.New("pitot: custom DeepSeek endpoint requires model and API key environment reference")
		}
		patch += "- id: acp\n  config:\n    provider: pitot-custom\n    model: " + quoted(options.Model) + "\n- id: llm-pi-ai\n  config:\n    providers:\n      pitot-custom:\n        api: openai-completions\n        baseURL: " + quoted(options.BaseURL) + "\n        apiKeyEnv: " + quoted(options.APIKeyEnv) + "\n        models:\n          - id: " + quoted(options.Model) + "\n            contextWindow: 32768\n            maxTokens: 4096\n        retryPolicy:\n          mode: normal\n          maxRetries: 0\n"
	} else if options.Model != "" {
		patch += "- id: acp\n  config:\n    provider: deepseek-official\n    model: " + quoted(options.Model) + "\n"
	}
	patchPath := filepath.Join(root, "control.patch.yml")
	if err := os.WriteFile(patchPath, []byte(patch), 0o600); err != nil {
		return err
	}
	args := append(append([]string{}, options.Arguments...), "--profile", "pitot-owned", "--from-default-profile", "acp", "--patch", patchPath)
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(childCtx, options.Executable, args...)
	cmd.Dir = cwd
	// The release gives its own disposal/finalization path five seconds. Keep
	// the outer deadline longer so native detached ranges can be finalized.
	cmd.WaitDelay = 7 * time.Second
	attach, release, err := ownProcess(childCtx, cmd)
	if err != nil {
		return err
	}
	defer release()
	cmd.Env = ownedEnvironment(root)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := attach(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	defer stdin.Close()
	ready := make(chan struct{})
	readErrors := make(chan error, 2)
	messages := make(chan map[string]json.RawMessage, 32)
	var once sync.Once
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			var marker struct {
				Event string `json:"event"`
				Nonce string `json:"nonce"`
			}
			_, parseErr := protocol.Parse(scanner.Bytes())
			if parseErr == nil && json.Unmarshal(scanner.Bytes(), &marker) == nil && marker.Event == "pitot-shell-control-ready" && marker.Nonce == nonce {
				once.Do(func() { close(ready) })
				continue
			}
			_, _ = fmt.Fprintln(options.Stderr, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			readErrors <- err
		}
	}()
	go func() {
		defer close(messages)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64<<10), 4<<20)
		for scanner.Scan() {
			var message map[string]json.RawMessage
			_, parseErr := protocol.Parse(scanner.Bytes())
			if parseErr != nil || json.Unmarshal(scanner.Bytes(), &message) != nil {
				readErrors <- errors.New("invalid DeepSeek ACP JSON")
				return
			}
			select {
			case messages <- message:
			case <-childCtx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			readErrors <- err
		}
		select {
		case <-ready:
		default:
			readErrors <- errors.New("DeepSeek closed before plugin readiness")
		}
	}()
	runErr := func() error {
		select {
		case <-ready:
		case err := <-readErrors:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(30 * time.Second):
			return errors.New("DeepSeek native plugin readiness timed out")
		}
		nextID := 0
		activeSession := ""
		write := func(value any) error { return json.NewEncoder(stdin).Encode(value) }
		call := func(method string, params any) (json.RawMessage, error) {
			nextID++
			id := nextID
			if err := write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
				return nil, err
			}
			timer := time.NewTimer(5 * time.Minute)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-timer.C:
					return nil, errors.New("DeepSeek ACP request timed out")
				case err := <-readErrors:
					return nil, err
				case message, open := <-messages:
					if !open {
						return nil, errors.New("DeepSeek closed the ACP stream")
					}
					var version string
					_ = json.Unmarshal(message["jsonrpc"], &version)
					if version != "2.0" {
						return nil, errors.New("invalid DeepSeek JSON-RPC version")
					}
					var inbound string
					_ = json.Unmarshal(message["method"], &inbound)
					if inbound != "" {
						if rawID, hasID := message["id"]; hasID {
							if inbound == "session/request_permission" {
								if err := write(map[string]any{"jsonrpc": "2.0", "id": rawID, "result": map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}}); err != nil {
									return nil, err
								}
							} else if err := write(map[string]any{"jsonrpc": "2.0", "id": rawID, "error": map[string]any{"code": -32601, "message": "unsupported client method"}}); err != nil {
								return nil, err
							}
						}
						if inbound == "session/update" {
							var update struct {
								SessionID string `json:"sessionId"`
								Update    struct {
									Kind    string          `json:"sessionUpdate"`
									Content json.RawMessage `json:"content"`
								} `json:"update"`
							}
							if json.Unmarshal(message["params"], &update) != nil {
								return nil, errors.New("invalid DeepSeek session update")
							}
							if activeSession != "" && update.SessionID != activeSession {
								return nil, errors.New("uncorrelated DeepSeek session update")
							}
							if activeSession != "" && update.Update.Kind == "agent_message_chunk" {
								var content struct {
									Type string `json:"type"`
									Text string `json:"text"`
								}
								if json.Unmarshal(update.Update.Content, &content) != nil {
									return nil, errors.New("invalid DeepSeek message chunk")
								}
								if content.Type == "text" {
									if _, err := io.WriteString(options.Stdout, content.Text); err != nil {
										return nil, err
									}
								}
							}
						}
						continue
					}
					var responseID int
					if json.Unmarshal(message["id"], &responseID) != nil || responseID != id {
						return nil, errors.New("uncorrelated DeepSeek ACP response")
					}
					if _, failed := message["error"]; failed {
						return nil, fmt.Errorf("DeepSeek ACP %s failed", method)
					}
					return message["result"], nil
				}
			}
		}
		v, err := call("initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}, "clientInfo": map[string]string{"name": "pitot", "version": "1"}})
		if err != nil {
			return err
		}
		var initialized struct {
			Version int `json:"protocolVersion"`
		}
		if json.Unmarshal(v, &initialized) != nil || initialized.Version != 1 {
			return errors.New("unsupported DeepSeek ACP version")
		}
		v, err = call("session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}})
		if err != nil {
			return err
		}
		var session struct {
			ID string `json:"sessionId"`
		}
		if json.Unmarshal(v, &session) != nil || session.ID == "" {
			return errors.New("DeepSeek omitted session identity")
		}
		activeSession = session.ID
		v, err = call("session/prompt", map[string]any{"sessionId": session.ID, "prompt": []map[string]string{{"type": "text", "text": options.Prompt}}})
		if err != nil {
			return err
		}
		var prompt struct {
			Stop string `json:"stopReason"`
		}
		if json.Unmarshal(v, &prompt) != nil || prompt.Stop == "" {
			return errors.New("DeepSeek omitted prompt stop reason")
		}
		_, err = call("session/close", map[string]string{"sessionId": session.ID})
		return err
	}()
	_ = stdin.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if runErr == nil && err != nil {
			runErr = err
		}
	case <-time.After(6 * time.Second):
		cancel()
		waitErr := <-done
		if runErr == nil {
			runErr = errors.Join(errors.New("DeepSeek shutdown exceeded its grace period"), waitErr)
		}
	}
	return runErr
}
