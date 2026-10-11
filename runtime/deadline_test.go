package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/operatorstack/pitot/bridge"
	"github.com/operatorstack/pitot/config"
	"github.com/operatorstack/pitot/protocol"
	"github.com/operatorstack/pitot/schema"
)

// Run an actual child process so blocked writes exercise operating-system pipes.
func TestControllerDeadlineProcess(t *testing.T) {
	mode := os.Getenv("PITOT_DEADLINE_TEST_PROCESS")
	if mode == "" {
		return
	}
	if mode == "unread" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	scanner := protocol.NewReader(os.Stdin)
	first := true
	for scanner.Scan() {
		var request schema.ControlRequested
		if err := protocol.DecodeLine(scanner.Bytes(), &request); err != nil {
			os.Exit(2)
		}
		if path := os.Getenv("PITOT_DEADLINE_TEST_RECEIPT"); path != "" {
			file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				os.Exit(2)
			}
			_, err = file.WriteString(request.ActionID + "\n")
			closeErr := file.Close()
			if err != nil || closeErr != nil {
				os.Exit(2)
			}
		}
		if mode == "timeout" {
			time.Sleep(30 * time.Second)
		}
		if mode == "late" && first {
			time.Sleep(600 * time.Millisecond)
		}
		if mode == "delayed-deny" {
			time.Sleep(300 * time.Millisecond)
		}
		first = false
		response := schema.ControlResponse{
			PitotVersion: schema.Version, Type: schema.TypeControlResponse,
			ControllerID: "policy", ActionID: request.ActionID, Outcome: schema.OutcomeAllow,
		}
		if mode == "delayed-deny" {
			response.Outcome = schema.OutcomeDeny
		}
		if err := protocol.WriteLine(os.Stdout, response); err != nil {
			os.Exit(2)
		}
	}
	os.Exit(0)
}

func deadlineManager(t *testing.T, mode string, deadlineMS int) (*Manager, string) {
	t.Helper()
	t.Setenv("PITOT_DEADLINE_TEST_PROCESS", mode)
	receipt := filepath.Join(t.TempDir(), "request.txt")
	t.Setenv("PITOT_DEADLINE_TEST_RECEIPT", receipt)
	manager, err := Start(context.Background(), config.Config{Controllers: map[string]config.ControllerConfig{
		"shell": {
			ID: "policy", Command: []string{os.Args[0], "-test.run=^TestControllerDeadlineProcess$"},
			DeadlineMS: deadlineMS, OnTimeout: schema.OutcomeAllow, OnUnavailable: schema.OutcomeDeny,
		},
	}}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	return manager, receipt
}

func deadlineRequest(id string, payloadSize int) schema.ControlRequested {
	data, _ := json.Marshal(strings.Repeat("x", payloadSize))
	return schema.ControlRequested{
		PitotVersion: schema.Version, Type: schema.TypeControlRequested,
		Kind: "shell", ActionID: id, Data: data,
	}
}

func boundedRequest(t *testing.T, manager *Manager, ctx context.Context, request schema.ControlRequested, limit time.Duration) controllerResult {
	t.Helper()
	results := make(chan controllerResult, 1)
	go func() {
		response, err := manager.Request(ctx, request)
		results <- controllerResult{response: response, err: err}
	}()
	select {
	case result := <-results:
		return result
	case <-time.After(limit):
		t.Fatalf("request %s exceeded %s despite its deadline", request.ActionID, limit)
		return controllerResult{}
	}
}

func TestControllerDeadlineBoundsUnreadPipe(t *testing.T) {
	for _, callerDeadline := range []bool{false, true} {
		name := "configured"
		if callerDeadline {
			name = "caller"
		}
		t.Run(name, func(t *testing.T) {
			deadlineMS := 60
			if callerDeadline {
				deadlineMS = 5000
			}
			manager, _ := deadlineManager(t, "unread", deadlineMS)
			request := deadlineRequest("blocked", 1024*1024)
			ctx := context.Background()
			if callerDeadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 200*time.Millisecond)
				defer cancel()
			}
			result := boundedRequest(t, manager, ctx, request, time.Second)
			if result.response.ActionID != "blocked" || result.response.Outcome != schema.OutcomeAllow {
				t.Fatalf("timeout default = %+v, error = %v", result.response, result.err)
			}
			if callerDeadline && !errors.Is(result.err, context.DeadlineExceeded) {
				t.Fatalf("caller deadline error = %v", result.err)
			}
			// An interrupted frame cannot safely share its pipe with a new request.
			next := boundedRequest(t, manager, context.Background(), deadlineRequest("next", 1), time.Second)
			if next.response.Outcome != schema.OutcomeDeny {
				t.Fatalf("interrupted transport was reused: %+v", next)
			}
			select {
			case <-manager.controllers["shell"].done:
			case <-time.After(time.Second):
				t.Fatal("stalled controller process survived its interrupted write")
			}
		})
	}
}

func TestControllerDeadlineBoundsQueue(t *testing.T) {
	for _, callerDeadline := range []bool{false, true} {
		name, deadlineMS, limit := "configured", 750, 1100*time.Millisecond
		if callerDeadline {
			name, deadlineMS, limit = "caller", 5000, time.Second
		}
		t.Run(name, func(t *testing.T) {
			manager, receipt := deadlineManager(t, "timeout", deadlineMS)
			first := make(chan controllerResult, 1)
			go func() {
				response, err := manager.Request(context.Background(), deadlineRequest("first", 1))
				first <- controllerResult{response: response, err: err}
			}()
			waitFor(t, receipt, "first")
			ctx := context.Background()
			if callerDeadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 40*time.Millisecond)
				defer cancel()
			}
			result := boundedRequest(t, manager, ctx, deadlineRequest("queued", 1), limit)
			if result.response.ActionID != "queued" || result.response.Outcome != schema.OutcomeAllow {
				t.Fatalf("queued timeout default = %+v, error = %v", result.response, result.err)
			}
			if callerDeadline && !errors.Is(result.err, context.DeadlineExceeded) {
				t.Fatalf("queued caller deadline error = %v", result.err)
			}
			manager.Close()
			select {
			case <-first:
			case <-time.After(time.Second):
				t.Fatal("first request survived runtime shutdown")
			}
		})
	}
}

func TestControllerDeadlineDiscardsLateResponse(t *testing.T) {
	manager, _ := deadlineManager(t, "late", 400)
	first := boundedRequest(t, manager, context.Background(), deadlineRequest("expired", 1), time.Second)
	if first.response.Outcome != schema.OutcomeAllow || first.response.ActionID != "expired" {
		t.Fatalf("first timeout = %+v", first)
	}
	second := boundedRequest(t, manager, context.Background(), deadlineRequest("current", 1), time.Second)
	if second.err != nil || second.response.Outcome != schema.OutcomeAllow || second.response.ActionID != "current" {
		t.Fatalf("late response resolved the current request: %+v", second)
	}
}

func TestControllerDeadlineDoesNotSendCanceledRequest(t *testing.T) {
	manager, receipt := deadlineManager(t, "allow", 2000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := boundedRequest(t, manager, ctx, deadlineRequest("canceled", 1), time.Second)
	if !errors.Is(result.err, context.Canceled) || result.response.Outcome != schema.OutcomeAllow {
		t.Fatalf("canceled request = %+v", result)
	}
	current := boundedRequest(t, manager, context.Background(), deadlineRequest("current", 1), time.Second)
	if current.err != nil || current.response.ActionID != "current" {
		t.Fatalf("current request = %+v", current)
	}
	if observed := waitFor(t, receipt, "current"); strings.Contains(observed, "canceled") {
		t.Fatalf("an already canceled request reached the Controller: %s", observed)
	}
}

func TestControllerDeadlineCanceledDuplicatePreservesOriginal(t *testing.T) {
	manager, receipt := deadlineManager(t, "delayed-deny", 2000)
	request := deadlineRequest("same", 1)
	original := make(chan controllerResult, 1)
	go func() {
		response, err := manager.Request(context.Background(), request)
		original <- controllerResult{response: response, err: err}
	}()
	waitFor(t, receipt, "same")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	duplicate := boundedRequest(t, manager, ctx, request, time.Second)
	if !errors.Is(duplicate.err, bridge.ErrDuplicate) || duplicate.response.Outcome != "" {
		t.Fatalf("duplicate was allowed to resolve the original action: %+v", duplicate)
	}
	select {
	case result := <-original:
		if result.err != nil || result.response.ActionID != "same" || result.response.Outcome != schema.OutcomeDeny {
			t.Fatalf("original decision was lost: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("original request did not resolve")
	}
}
