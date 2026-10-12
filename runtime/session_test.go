package runtime

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/operatorstack/pitot/config"
	"github.com/operatorstack/pitot/projection"
	"github.com/operatorstack/pitot/schema"
)

func TestSessionIdentityReachesControllerAndConsumer(t *testing.T) {
	helper := buildTestRole(t)
	for _, mode := range []projection.Mode{projection.Full, projection.SHA256, projection.Omit} {
		t.Run(string(mode), func(t *testing.T) {
			directory := t.TempDir()
			controllerPath := filepath.Join(directory, "controller.jsonl")
			consumerPath := filepath.Join(directory, "consumer.jsonl")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			manager, err := Start(ctx, config.Config{
				Consumers:   []config.ConsumerConfig{{ID: "audit", Command: []string{helper, "--role", "consumer", "--receipt", consumerPath}, Events: []string{schema.TypeActionRequested}, Projection: config.ProjectionConfig{Content: mode}}},
				Controllers: map[string]config.ControllerConfig{"shell": {ID: "policy", Command: []string{helper, "--role", "controller", "--id", "policy", "--receipt", controllerPath}, DeadlineMS: 2000, OnTimeout: schema.OutcomeDeny, OnUnavailable: schema.OutcomeDeny}},
			}, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			sessions := []string{"雪/first-session", " opaque second session ", ""}
			for index, session := range sessions {
				id := []string{"act_first", "act_second", "act_missing"}[index]
				command := "allow"
				want := schema.OutcomeAllow
				if index == 1 {
					command, want = "PITOT_DENY", schema.OutcomeDeny
				}
				event := actionEvent(t, id, command)
				event.SessionID = session
				response, err := manager.DeliverEvent(ctx, event)
				if err != nil || response == nil || response.ActionID != id || response.Outcome != want {
					t.Fatalf("control response=%+v error=%v", response, err)
				}
			}
			consumerRows := strings.Split(strings.TrimSpace(waitFor(t, consumerPath, "act_missing")), "\n")
			controllerRows := strings.Split(strings.TrimSpace(waitFor(t, controllerPath, "act_missing")), "\n")
			var requests []schema.ControlRequested
			for _, row := range controllerRows {
				var receipt struct {
					Type  string                  `json:"receipt_type"`
					Value schema.ControlRequested `json:"value"`
				}
				if err := json.Unmarshal([]byte(row), &receipt); err != nil {
					t.Fatal(err)
				}
				if receipt.Type == "request" {
					requests = append(requests, receipt.Value)
				}
			}
			if len(requests) != 3 || len(consumerRows) != 3 {
				t.Fatalf("requests=%d consumers=%d", len(requests), len(consumerRows))
			}
			for index, request := range requests {
				var controller, consumer schema.Event
				if err := json.Unmarshal(request.Data, &controller); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(consumerRows[index]), &consumer); err != nil {
					t.Fatal(err)
				}
				if controller.SessionID != sessions[index] || consumer.SessionID != sessions[index] || controller.Host != consumer.Host || controller.Observation != consumer.Observation || consumer.Action.ID != request.ActionID || consumer.Content.Mode != string(mode) {
					t.Fatalf("identity changed: controller=%+v consumer=%+v", controller, consumer)
				}
				if index == 2 && (strings.Contains(string(request.Data), "session_id") || strings.Contains(consumerRows[index], "session_id")) {
					t.Fatal("missing identity was manufactured")
				}
			}
		})
	}
}
