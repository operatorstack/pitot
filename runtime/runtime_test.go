package runtime

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/operatorstack/pitot/config"
	"github.com/operatorstack/pitot/projection"
	"github.com/operatorstack/pitot/schema"
)

func buildTestRole(t *testing.T) string {
	t.Helper()
	name := "pitot-testrole"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	command := exec.Command("go", "build", "-o", path, "../internal/testrole")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build test role: %v\n%s", err, output)
	}
	return path
}

func actionEvent(t *testing.T, id, command string) schema.Event {
	t.Helper()
	content, err := projection.Apply(projection.Full, []byte(command))
	if err != nil {
		t.Fatal(err)
	}
	return schema.Event{
		PitotVersion: schema.Version,
		Type:         schema.TypeActionRequested,
		Host:         schema.Host{Name: "claude"},
		Action:       &schema.Action{ID: id, Kind: "shell"},
		Content:      &content,
		Observation:  schema.Observation{Source: schema.SourceHostHook, Fidelity: schema.FidelityDirect},
	}
}

func waitFor(t *testing.T, path, contains string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(path)
		if strings.Contains(string(raw), contains) {
			return string(raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not contain %q", path, contains)
	return ""
}

func TestRuntimeSharesHookConsumerAndRequestBoundary(t *testing.T) {
	helper := buildTestRole(t)
	dir := t.TempDir()
	consumerReceipt := filepath.Join(dir, "consumer.jsonl")
	controllerReceipt := filepath.Join(dir, "controller.jsonl")
	requestReceipt := filepath.Join(dir, "request.jsonl")
	cfg := config.Config{
		Consumers: []config.ConsumerConfig{{
			ID: "audit", Command: []string{helper, "--role", "consumer", "--receipt", consumerReceipt},
			Events: []string{schema.TypeActionRequested}, Projection: config.ProjectionConfig{Content: projection.SHA256},
		}},
		Controllers: map[string]config.ControllerConfig{
			"shell":            {ID: "shell-policy", Command: []string{helper, "--role", "controller", "--id", "shell-policy", "--receipt", controllerReceipt, "--nonce", "abc"}, DeadlineMS: 2000, OnTimeout: schema.OutcomeDeny, OnUnavailable: schema.OutcomeDeny},
			"release.approval": {ID: "release-policy", Command: []string{helper, "--role", "controller", "--id", "release-policy", "--receipt", requestReceipt, "--nonce", "abc"}, DeadlineMS: 2000, OnTimeout: schema.OutcomeDeny, OnUnavailable: schema.OutcomeDeny},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager, err := Start(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	runtimePath := filepath.Join(dir, "runtime.json")
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- NewServer(manager, strings.Repeat("a", 64), runtimePath, io.Discard, io.Discard).Serve(ctx)
	}()
	for i := 0; i < 250; i++ {
		if _, err := os.Stat(runtimePath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	client, err := OpenClient(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	allow, err := client.DeliverEvent(ctx, actionEvent(t, "act_allow", "PITOT_ALLOW abc"))
	if err != nil || allow == nil || allow.Outcome != schema.OutcomeAllow {
		t.Fatalf("allow = %+v, err = %v", allow, err)
	}
	deny, err := client.DeliverEvent(ctx, actionEvent(t, "act_deny", "PITOT_DENY abc"))
	if err != nil || deny == nil || deny.Outcome != schema.OutcomeDeny || !strings.Contains(deny.Message, "abc") {
		t.Fatalf("deny = %+v, err = %v", deny, err)
	}
	consumer := waitFor(t, consumerReceipt, "act_deny")
	if strings.Contains(consumer, "PITOT_ALLOW") || !strings.Contains(consumer, `"mode":"sha256"`) {
		t.Fatalf("consumer projection leaked content or missed digest: %s", consumer)
	}
	response, err := client.Request(ctx, schema.ControlRequested{PitotVersion: schema.Version, Type: schema.TypeControlRequested, Kind: "release.approval", ActionID: "act_request", Data: json.RawMessage(`{"release":"PITOT_DENY abc"}`)})
	if err != nil || response.Outcome != schema.OutcomeDeny || response.ControllerID != "release-policy" {
		t.Fatalf("request response = %+v, err = %v", response, err)
	}
	waitFor(t, requestReceipt, "act_request")
	cancel()
	if err := <-serveErr; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runtimePath); !os.IsNotExist(err) {
		t.Fatalf("runtime descriptor was not removed: %v", err)
	}
}

func TestUnavailableAndTimeoutUseDeclaredDefaults(t *testing.T) {
	helper := buildTestRole(t)
	tests := []struct {
		name    string
		command []string
	}{
		{"unavailable", []string{filepath.Join(t.TempDir(), "missing-controller")}},
		{"timeout", []string{helper, "--role", "controller", "--mode", "timeout"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			manager, err := Start(ctx, config.Config{Controllers: map[string]config.ControllerConfig{
				"shell": {ID: "policy", Command: test.command, DeadlineMS: 30, OnTimeout: schema.OutcomeDeny, OnUnavailable: schema.OutcomeDeny},
			}}, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			response, _ := manager.DeliverEvent(ctx, actionEvent(t, "act_default", "echo ok"))
			if response == nil || response.Outcome != schema.OutcomeDeny {
				t.Fatalf("response = %+v", response)
			}
		})
	}
}

func TestProjectEventModesAndCannotWiden(t *testing.T) {
	event := actionEvent(t, "act_projection", "secret")
	for _, mode := range []projection.Mode{projection.Full, projection.SHA256, projection.Omit} {
		projected, err := projectEvent(event, mode)
		if err != nil || projected.Content.Mode != string(mode) {
			t.Fatalf("mode %s: %+v, %v", mode, projected.Content, err)
		}
	}
	hashed, _ := projectEvent(event, projection.SHA256)
	if _, err := projectEvent(hashed, projection.Full); err == nil {
		t.Fatal("expected widening a hash projection to fail")
	}
}

func TestConsumerFilterAndBoundedQueue(t *testing.T) {
	worker := &consumerWorker{
		id: "audit", events: map[string]struct{}{schema.TypeActionRequested: {}},
		projection: projection.Omit, queue: make(chan schema.Event, 1), dead: make(chan struct{}),
	}
	ignored := actionEvent(t, "act_ignored", "secret")
	ignored.Type = "other.event"
	if err := worker.offer(ignored); err != nil || len(worker.queue) != 0 {
		t.Fatalf("filtered event err=%v queue=%d", err, len(worker.queue))
	}
	if err := worker.offer(actionEvent(t, "act_one", "secret")); err != nil {
		t.Fatal(err)
	}
	if err := worker.offer(actionEvent(t, "act_two", "secret")); err == nil || !strings.Contains(err.Error(), "queue is full") {
		t.Fatalf("full queue error = %v", err)
	}
}
