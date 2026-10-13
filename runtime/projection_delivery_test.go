package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/operatorstack/pitot/config"
	"github.com/operatorstack/pitot/projection"
	"github.com/operatorstack/pitot/schema"
)

func queuedManager(modes ...projection.Mode) *Manager {
	m := &Manager{stderr: io.Discard, controllers: map[string]*controllerWorker{}, observedKinds: map[string]struct{}{"shell": {}}}
	for i, mode := range modes {
		m.consumers = append(m.consumers, &consumerWorker{
			id: fmt.Sprintf("observer-%d", i), projection: mode,
			events: map[string]struct{}{schema.TypeActionRequested: {}},
			queue:  make(chan schema.Event, 1), dead: make(chan struct{}),
		})
	}
	return m
}

func TestRepeatedConsumerProjectionSemantics(t *testing.T) {
	digest := sha256.Sum256([]byte("a/<>&\u2028"))
	full := &schema.Content{Mode: schema.ContentFull, Full: json.RawMessage(" \n \"\\u0061\\/<>&\\u2028\" \t")}
	for _, test := range []struct {
		name  string
		input *schema.Content
		mode  projection.Mode
		want  *schema.Content
	}{
		{"full-normalization", full, projection.Full, &schema.Content{Mode: schema.ContentFull, Full: json.RawMessage(`"a/\u003c\u003e\u0026\u2028"`)}},
		{"decoded-digest", full, projection.SHA256, &schema.Content{Mode: schema.ContentSHA256, SHA256: hex.EncodeToString(digest[:])}},
		{"omit", full, projection.Omit, &schema.Content{Mode: schema.ContentOmit}},
		{"surrogate-normalization", &schema.Content{Mode: schema.ContentFull, Full: json.RawMessage(`"\ud800"`)}, projection.Full, &schema.Content{Mode: schema.ContentFull, Full: json.RawMessage(`"�"`)}},
		{"digest-case", &schema.Content{Mode: schema.ContentSHA256, SHA256: strings.Repeat("AB", 32)}, projection.SHA256, &schema.Content{Mode: schema.ContentSHA256, SHA256: strings.Repeat("AB", 32)}},
		{"digest-narrowing", &schema.Content{Mode: schema.ContentSHA256, SHA256: strings.Repeat("ab", 32)}, projection.Omit, &schema.Content{Mode: schema.ContentOmit}},
		{"already-omitted", &schema.Content{Mode: schema.ContentOmit}, projection.Omit, &schema.Content{Mode: schema.ContentOmit}},
		{"nil-full", nil, projection.Full, nil},
		{"nil-sha", nil, projection.SHA256, nil},
		{"nil-omit", nil, projection.Omit, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := queuedManager(test.mode, test.mode)
			event := actionEvent(t, "act_project", "unused")
			event.SessionID = "session-opaque"
			event.Content = test.input
			before, _ := json.Marshal(event)
			if response, err := m.DeliverEvent(context.Background(), event); err != nil || response != nil {
				t.Fatalf("delivery = %+v, %v", response, err)
			}
			for _, consumer := range m.consumers {
				got := <-consumer.queue
				if (got.Content == nil) != (test.want == nil) || (got.Content != nil && (got.Content.Mode != test.want.Mode || got.Content.SHA256 != test.want.SHA256 || !bytes.Equal(got.Content.Full, test.want.Full))) {
					t.Fatalf("content = %+v, want %+v", got.Content, test.want)
				}
				got.Content = event.Content
				if !reflect.DeepEqual(got, event) {
					t.Fatal("projection changed event metadata")
				}
			}
			after, _ := json.Marshal(event)
			if !bytes.Equal(before, after) {
				t.Fatal("input event mutated")
			}
		})
	}
}

func TestConsumerProjectionContentOwnershipAndEventIsolation(t *testing.T) {
	m := queuedManager(projection.Full, projection.Full, projection.SHA256, projection.SHA256, projection.Omit, projection.Omit)
	for _, command := range []string{"first secret", "second secret"} {
		event := actionEvent(t, "act_"+command, command)
		if _, err := m.DeliverEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		first := <-m.consumers[0].queue
		second := <-m.consumers[1].queue
		want, _ := json.Marshal(command)
		event.Content.Full[1] = 'X'
		first.Content.Full[1] = 'Y'
		first.Content.Mode = schema.ContentOmit
		if !bytes.Equal(second.Content.Full, want) || second.Content.Mode != schema.ContentFull {
			t.Fatal("queued full content aliases input or another consumer")
		}
		one := <-m.consumers[2].queue
		two := <-m.consumers[3].queue
		one.Content.SHA256 = "changed"
		digest := sha256.Sum256([]byte(command))
		if two.Content.SHA256 != hex.EncodeToString(digest[:]) {
			t.Fatal("digest changed or cached a previous event")
		}
		one = <-m.consumers[4].queue
		two = <-m.consumers[5].queue
		one.Content.Mode = schema.ContentFull
		if two.Content.Mode != schema.ContentOmit {
			t.Fatal("queued content values alias")
		}
	}
}

func TestConsumerProjectionSubscriptionsQueuesAndFaults(t *testing.T) {
	m := queuedManager(projection.Full, projection.Full, projection.Omit, projection.Omit, projection.Omit)
	var diagnostics bytes.Buffer
	m.stderr = &diagnostics
	// Full consumers are not subscribed: the absent full representation must
	// not cause an eager widening fault, or receive an event.
	m.consumers[0].events = map[string]struct{}{}
	m.consumers[1].events = map[string]struct{}{}
	close(m.consumers[2].dead)
	m.consumers[3].queue <- schema.Event{}
	event := actionEvent(t, "act_filter", "unused")
	event.Content = &schema.Content{Mode: schema.ContentSHA256, SHA256: strings.Repeat("ab", 32)}
	if _, err := m.DeliverEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{0, 1, 2} {
		if len(m.consumers[index].queue) != 0 {
			t.Fatalf("unexpected delivery to consumer %d", index)
		}
	}
	if got := <-m.consumers[3].queue; got.Type != "" {
		t.Fatal("full queue replaced its existing event")
	}
	if got := <-m.consumers[4].queue; got.Content.Mode != schema.ContentOmit {
		t.Fatal("available consumer did not receive narrowed event")
	}
	if !strings.Contains(diagnostics.String(), "consumer process is unavailable") || !strings.Contains(diagnostics.String(), "consumer queue is full") || strings.Contains(diagnostics.String(), "widen") {
		t.Fatalf("diagnostics = %s", &diagnostics)
	}
	// Subscribed consumers with unavailable representations each report faults.
	m = queuedManager(projection.Full, projection.Full)
	diagnostics.Reset()
	m.stderr = &diagnostics
	if _, err := m.DeliverEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if strings.Count(diagnostics.String(), "cannot widen an already projected event") != 2 {
		t.Fatalf("expected two widening faults: %s", &diagnostics)
	}
	if len(m.consumers[0].queue)+len(m.consumers[1].queue) != 0 {
		t.Fatal("unavailable content delivered")
	}
}

func TestMalformedContentRejectedBeforeCachedOmission(t *testing.T) {
	for _, raw := range []string{`null`, `1`, `{}`, `"unterminated`} {
		m := queuedManager(projection.Omit, projection.Omit)
		event := actionEvent(t, "act_invalid", "unused")
		event.Content.Full = json.RawMessage(raw)
		if _, err := m.DeliverEvent(context.Background(), event); err != schema.ErrInvalidEvent {
			t.Fatalf("%s: %v", raw, err)
		}
		if len(m.consumers[0].queue)+len(m.consumers[1].queue) != 0 {
			t.Fatal("malformed content was omitted and delivered")
		}
	}
}

func TestRepeatedProjectionPreservesActualControllerAndConsumers(t *testing.T) {
	helper := buildTestRole(t)
	dir := t.TempDir()
	controllerReceipt := filepath.Join(dir, "controller.jsonl")
	cfg := config.Config{Controllers: map[string]config.ControllerConfig{"shell": {
		ID: "policy", Command: []string{helper, "--role", "controller", "--id", "policy", "--receipt", controllerReceipt},
		DeadlineMS: 2000, OnTimeout: schema.OutcomeDeny, OnUnavailable: schema.OutcomeDeny,
	}}}
	modes := []projection.Mode{projection.Full, projection.Full, projection.SHA256, projection.SHA256, projection.Omit, projection.Omit}
	for i, mode := range modes {
		cfg.Consumers = append(cfg.Consumers, config.ConsumerConfig{
			ID: fmt.Sprintf("observer-%d", i), Command: []string{helper, "--role", "consumer", "--receipt", filepath.Join(dir, fmt.Sprintf("consumer-%d.jsonl", i))},
			Events: []string{schema.TypeActionRequested}, Projection: config.ProjectionConfig{Content: mode},
		})
	}
	m, err := Start(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for _, outcome := range []string{schema.OutcomeAllow, schema.OutcomeDeny} {
		marker := "PITOT_" + strings.ToUpper(outcome)
		event := actionEvent(t, "act_actual_"+outcome, "unused")
		event.SessionID = "opaque-native-session"
		event.Content.Full = json.RawMessage(`"` + marker + ` \u0061bc"`)
		response, err := m.DeliverControlledEvent(context.Background(), event)
		if err != nil || response == nil || response.Outcome != outcome {
			t.Fatalf("control = %+v, %v", response, err)
		}
		for i, mode := range modes {
			raw := waitFor(t, filepath.Join(dir, fmt.Sprintf("consumer-%d.jsonl", i)), event.Action.ID)
			var got schema.Event
			for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
				if err := json.Unmarshal([]byte(line), &got); err != nil {
					t.Fatal(err)
				}
				if got.Action.ID == event.Action.ID {
					break
				}
			}
			if got.SessionID != event.SessionID || got.Content.Mode != string(mode) {
				t.Fatalf("consumer metadata/policy = %+v", got)
			}
			if mode == projection.Full && !bytes.Equal(got.Content.Full, json.RawMessage(`"`+marker+` abc"`)) {
				t.Fatalf("normalized full content = %s", got.Content.Full)
			}
			if mode != projection.Full && strings.Contains(raw, marker) {
				t.Fatal("projected consumer leaked raw command")
			}
		}
		raw := waitFor(t, controllerReceipt, event.Action.ID)
		var original schema.Event
		found := false
		for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
			var receipt struct {
				Type  string                  `json:"receipt_type"`
				Value schema.ControlRequested `json:"value"`
			}
			if err := json.Unmarshal([]byte(line), &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Type == "request" && receipt.Value.ActionID == event.Action.ID {
				if err := json.Unmarshal(receipt.Value.Data, &original); err != nil {
					t.Fatal(err)
				}
				found = true
				break
			}
		}
		if !found || !reflect.DeepEqual(original, event) {
			t.Fatalf("controller event changed: %+v", original)
		}
	}
}
