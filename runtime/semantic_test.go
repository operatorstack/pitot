package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/operatorstack/pitot/config"
	"github.com/operatorstack/pitot/projection"
	"github.com/operatorstack/pitot/schema"
)

func TestMalformedContentCannotCrossConsumerBoundary(t *testing.T) {
	helper := buildTestRole(t)
	for _, mode := range []projection.Mode{projection.Omit, projection.SHA256, projection.Full} {
		t.Run(string(mode), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			dir := t.TempDir()
			receipt := filepath.Join(dir, "consumer.jsonl")
			manager, err := Start(ctx, config.Config{Consumers: []config.ConsumerConfig{{ID: "audit", Command: []string{helper, "--role", "consumer", "--receipt", receipt}, Events: []string{schema.TypeActionRequested}, Projection: config.ProjectionConfig{Content: mode}}}}, io.Discard)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			done := make(chan error, 1)
			runtimePath := filepath.Join(dir, "runtime.json")
			go func() {
				done <- NewServer(manager, strings.Repeat("a", 64), runtimePath, io.Discard, io.Discard).Serve(ctx)
			}()
			t.Cleanup(func() {
				cancel()
				manager.Close()
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(5 * time.Second):
					t.Error("server did not stop")
				}
			})
			var client *Client
			for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
				client, err = OpenClient(runtimePath)
				if err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil {
				t.Fatal(err)
			}
			bad := []schema.Content{{Mode: schema.ContentOmit, Full: json.RawMessage(`"SAMPLE_PAYLOAD"`)}, {Mode: schema.ContentSHA256, SHA256: strings.Repeat("a", 64), Full: json.RawMessage(`"SAMPLE_PAYLOAD"`)}, {Mode: schema.ContentFull, Full: json.RawMessage(`{"command":"SAMPLE_PAYLOAD"}`)}}
			for i, content := range bad {
				event := actionEvent(t, fmt.Sprintf("bad-%d", i), "unused")
				event.Content = &content
				if _, err := client.DeliverEvent(ctx, event); err == nil {
					t.Fatal("HTTP admitted malformed content")
				}
				if _, err := manager.DeliverEvent(ctx, event); err != schema.ErrInvalidEvent {
					t.Fatalf("typed admission error=%v", err)
				}
				if _, err := projectEvent(event, mode); err != schema.ErrInvalidContent {
					t.Fatalf("projection error=%v", err)
				}
			}
			// A valid FIFO barrier proves the real Consumer processed every
			// earlier offered line; rejected records must be absent, not delayed.
			if _, err := client.DeliverEvent(ctx, actionEvent(t, "barrier", "VALID_PAYLOAD")); err != nil {
				t.Fatal(err)
			}
			data := waitFor(t, receipt, "barrier")
			if strings.Contains(data, "bad-") || strings.Contains(data, "SAMPLE_PAYLOAD") {
				t.Fatalf("malformed event crossed boundary: %s", data)
			}
			if got := strings.Count(strings.TrimSpace(data), "\n") + 1; got != 1 {
				t.Fatalf("consumer received %d records", got)
			}
			if _, err := os.Stat(receipt); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTypedAdmissionPrecedesConsumerFanout(t *testing.T) {
	// A queued Consumer makes admission ordering deterministic, without a
	// process or scheduling assumption.
	worker := &consumerWorker{events: map[string]struct{}{schema.TypeActionRequested: {}}, queue: make(chan schema.Event, 1), projection: projection.Full}
	manager := &Manager{consumers: []*consumerWorker{worker}}
	for _, mutate := range []func(*schema.Event){func(e *schema.Event) { e.Host.Name = "" }, func(e *schema.Event) { e.Observation.Source = "unknown" }, func(e *schema.Event) { e.Observation.Fidelity = "unknown" }, func(e *schema.Event) { e.Action.ID = "" }, func(e *schema.Event) { e.Type = "unknown" }} {
		event := actionEvent(t, "valid", "sample")
		mutate(&event)
		if _, err := manager.DeliverEvent(context.Background(), event); err != schema.ErrInvalidEvent {
			t.Fatalf("error=%v", err)
		}
		if len(worker.queue) != 0 {
			t.Fatal("invalid event entered Consumer queue")
		}
	}
}

func TestValidUsageEnvelopeDoesNotEnableRuntimeIngress(t *testing.T) {
	worker := &consumerWorker{events: map[string]struct{}{schema.TypeModelUsage: {}}, queue: make(chan schema.Event, 1), projection: projection.Omit}
	manager := &Manager{consumers: []*consumerWorker{worker}}
	event := schema.Event{PitotVersion: schema.Version, Type: schema.TypeModelUsage, Host: schema.Host{Name: "extension-host"}, Usage: &schema.Usage{}, Observation: schema.Observation{Source: schema.SourceHostEvent, Fidelity: schema.FidelityDirect}}
	if err := schema.ValidateEvent(event); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.DeliverEvent(context.Background(), event); err == nil {
		t.Fatal("usage envelope unexpectedly admitted")
	}
	if len(worker.queue) != 0 {
		t.Fatal("unsupported event entered Consumer queue")
	}
}

func TestTypedRequestValidationPrecedesControllerLookup(t *testing.T) {
	manager := &Manager{}
	request := schema.ControlRequested{PitotVersion: schema.Version, Type: schema.TypeControlRequested, Kind: "custom.approval", ActionID: "act", Data: json.RawMessage(`SAMPLE_PAYLOAD`)}
	if _, err := manager.Request(context.Background(), request); err != schema.ErrInvalidRequest {
		t.Fatalf("error=%v", err)
	}
}
