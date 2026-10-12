package runtime

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/operatorstack/pitot/config"
	"github.com/operatorstack/pitot/protocol"
	"github.com/operatorstack/pitot/schema"
)

func TestControlledReplyProcess(t *testing.T) {
	mode := os.Getenv("PITOT_CONTROLLED_TEST_PROCESS")
	if mode == "" {
		return
	}
	r := protocol.NewReader(os.Stdin)
	for r.Scan() {
		var request schema.ControlRequested
		if protocol.DecodeLine(r.Bytes(), &request) != nil {
			os.Exit(2)
		}
		switch mode {
		case "exit":
			os.Exit(0)
		case "timeout":
			time.Sleep(30 * time.Second)
		case "malformed":
			_, _ = io.WriteString(os.Stdout, "not-json\n")
			continue
		}
		response := schema.ControlResponse{PitotVersion: schema.Version, Type: schema.TypeControlResponse, ControllerID: "policy", ActionID: request.ActionID, Outcome: schema.OutcomeAllow}
		if mode == "deny" {
			response.Outcome = schema.OutcomeDeny
		}
		if mode == "mismatch" {
			response.ActionID = "another-action"
		}
		if protocol.WriteLine(os.Stdout, response) != nil {
			os.Exit(2)
		}
	}
	os.Exit(0)
}

func TestControlledDeliveryRejectsFallbackAllow(t *testing.T) {
	for _, mode := range []string{"allow", "deny", "exit", "timeout", "malformed", "mismatch", "absent"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("PITOT_CONTROLLED_TEST_PROCESS", mode)
			cfg := config.Config{}
			if mode != "absent" {
				cfg.Controllers = map[string]config.ControllerConfig{"shell": {ID: "policy", Command: []string{os.Args[0], "-test.run=^TestControlledReplyProcess$"}, DeadlineMS: 1000, OnTimeout: schema.OutcomeAllow, OnUnavailable: schema.OutcomeAllow}}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m, err := Start(ctx, cfg, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			path := filepath.Join(t.TempDir(), "runtime.json")
			done := make(chan error, 1)
			go func() { done <- NewServer(m, strings.Repeat("a", 64), path, io.Discard, io.Discard).Serve(ctx) }()
			t.Cleanup(func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
			var client *Client
			for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
				client, err = OpenClient(path)
				if err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil {
				t.Fatal(err)
			}
			event := schema.Event{PitotVersion: schema.Version, Type: schema.TypeActionRequested, Host: schema.Host{Name: "deepseek"}, SessionID: "native-session", Action: &schema.Action{ID: "test-action", Kind: "shell"}, Content: &schema.Content{Mode: schema.ContentFull, Full: []byte(`"echo test"`)}, Observation: schema.Observation{Source: schema.SourceHostHook, Fidelity: schema.FidelityDirect}}
			response, err := client.DeliverControlledEvent(ctx, event)
			if mode == "allow" || mode == "deny" {
				if err != nil || response == nil || response.Outcome != mode {
					t.Fatalf("response=%+v err=%v", response, err)
				}
			} else if err == nil || response != nil {
				t.Fatalf("fallback admitted: response=%+v err=%v", response, err)
			}
		})
	}
}
