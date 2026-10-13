package runtime

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/operatorstack/pitot/config"
	"github.com/operatorstack/pitot/projection"
	"github.com/operatorstack/pitot/schema"
)

func TestStartValidatesBeforeLaunchingProcesses(t *testing.T) {
	cases := []struct {
		name   string
		change func(*config.Config)
	}{
		{"protocol", func(c *config.Config) { c.RequiresProtocol = "unsupported" }},
		{"unsupported event", func(c *config.Config) { c.Consumers[0].Events = []string{schema.TypeModelUsage} }},
		{"missing events", func(c *config.Config) { c.Consumers[0].Events = nil }},
		{"duplicate events", func(c *config.Config) {
			c.Consumers[0].Events = append(c.Consumers[0].Events, schema.TypeActionRequested)
		}},
		{"duplicate consumer", func(c *config.Config) { c.Consumers = append(c.Consumers, c.Consumers[0]) }},
		{"projection", func(c *config.Config) { c.Consumers[0].Projection.Content = "invalid" }},
		{"consumer command", func(c *config.Config) { c.Consumers[0].Command = nil }},
		{"controller command", func(c *config.Config) {
			v := c.Controllers["custom.approval"]
			v.Command = nil
			c.Controllers["custom.approval"] = v
		}},
		{"controller deadline", func(c *config.Config) {
			v := c.Controllers["custom.approval"]
			v.DeadlineMS = 0
			c.Controllers["custom.approval"] = v
		}},
		{"controller default", func(c *config.Config) {
			v := c.Controllers["custom.approval"]
			v.OnUnavailable = "invalid"
			c.Controllers["custom.approval"] = v
		}},
		{"duplicate controller identity", func(c *config.Config) { c.Controllers["another.kind"] = c.Controllers["custom.approval"] }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "started")
			command := []string{os.Args[0], "-test.run=^TestStartupMarkerProcess$", "--", marker}
			cfg := config.Config{Consumers: []config.ConsumerConfig{{ID: "audit", Command: command, Events: []string{schema.TypeActionRequested}, Projection: config.ProjectionConfig{Content: projection.Omit}}}, Controllers: map[string]config.ControllerConfig{"custom.approval": {ID: "policy", Command: command, DeadlineMS: 1000, OnTimeout: schema.OutcomeDeny, OnUnavailable: schema.OutcomeAllow}}}
			tt.change(&cfg)
			manager, err := Start(context.Background(), cfg, io.Discard)
			if manager != nil {
				manager.Close()
				t.Fatal("invalid configuration returned a manager")
			}
			if err == nil {
				t.Fatal("invalid configuration accepted")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("child process started before validation: %v", err)
			}
		})
	}
}

func TestStartupMarkerProcess(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			if err := os.WriteFile(os.Args[i+1], []byte("started"), 0600); err != nil {
				os.Exit(2)
			}
			os.Exit(0)
		}
	}
}

func TestStartEmptyRuntimeProtocol(t *testing.T) {
	for _, strict := range []bool{false, true} {
		cfg := config.Config{RequireController: strict, RequiresProtocol: schema.Version}
		if cfg.Validate() == nil {
			t.Fatal("file configuration unexpectedly accepts no registrations")
		}
		manager, err := Start(context.Background(), cfg, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		manager.Close()
	}
	manager, err := Start(context.Background(), config.Config{RequiresProtocol: "unsupported"}, io.Discard)
	if manager != nil {
		manager.Close()
		t.Fatal("unsupported protocol returned manager")
	}
	if err == nil {
		t.Fatal("empty runtime bypassed protocol validation")
	}
}

func TestStartCustomRequestDefaults(t *testing.T) {
	for _, outcome := range []string{schema.OutcomeAllow, schema.OutcomeDeny} {
		t.Run(outcome, func(t *testing.T) {
			cfg := config.Config{Controllers: map[string]config.ControllerConfig{"custom.approval": {ID: "policy", Command: []string{filepath.Join(t.TempDir(), "absent-program")}, DeadlineMS: 1000, OnTimeout: outcome, OnUnavailable: outcome}}}
			manager, err := Start(context.Background(), cfg, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			request, err := NewControlRequest("custom.approval", map[string]string{"task": "review"})
			if err != nil {
				t.Fatal(err)
			}
			response, err := manager.Request(context.Background(), request)
			if err == nil {
				t.Fatal("missing process must retain its diagnostic error alongside the fallback")
			}
			if response.Outcome != outcome || response.ActionID != request.ActionID {
				t.Fatalf("configured custom request fallback changed: %+v", response)
			}
		})
	}
}
