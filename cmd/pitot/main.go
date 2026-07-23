// Command pitot is the reference executable for Pitot's sensor and control transport.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/operatorstack/pitot/adapters"
	"github.com/operatorstack/pitot/config"
	"github.com/operatorstack/pitot/runtime"
	"github.com/operatorstack/pitot/schema"
	"github.com/operatorstack/pitot/sensor"
)

var errBlocked = errors.New("pitot: block")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runWithIO(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, errBlocked) {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	return runWithIO(context.Background(), args, os.Stdin, stdout, stderr)
}

func runWithIO(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "init":
		return runInit(args[1:], stdin, stdout, stderr)
	case "dev":
		return runDev(ctx, args[1:], stdout, stderr)
	case "doctor":
		return doctor(stdout)
	case "run":
		return runRuntime(ctx, args[1:], stdout, stderr)
	case "hook":
		return runHook(ctx, args[1:], stdin, stdout, stderr)
	case "request":
		return runRequest(ctx, args[1:], stdout)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage())
		return nil
	default:
		fmt.Fprintln(stderr, usage())
		return fmt.Errorf("pitot: unknown command %q", args[0])
	}
}

// runHook preserves observation-only behavior unless an authenticated runtime is selected.
func runHook(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("pitot: hook requires a host identifier (claude, codex, copilot, cursor, gemini, kimi, opencode, pi, qwen)")
	}
	host := adapters.Host(args[0])
	if !adapters.IsSupported(host) {
		return fmt.Errorf("pitot: unsupported hook host %q", host)
	}
	runtimePath, err := parseRuntimeFlag(args[1:], false)
	if err != nil {
		return err
	}
	payload, err := io.ReadAll(io.LimitReader(stdin, 4<<20+1))
	if err != nil {
		return fmt.Errorf("pitot: read stdin: %w", err)
	}
	if len(payload) > 4<<20 {
		fmt.Fprintln(stderr, "pitot: hook payload exceeds 4 MiB")
		return errBlocked
	}
	event, err := sensor.Decode(host, payload, "full")
	if err != nil {
		actionID, idErr := runtime.NewActionID()
		if idErr != nil {
			actionID = "act_hook"
		}
		if fault, ok := sensor.AsFault(err, actionID); ok {
			_ = json.NewEncoder(stderr).Encode(fault)
		} else {
			fmt.Fprintln(stderr, err.Error())
		}
		return errBlocked
	}
	actionID, err := runtime.NewActionID()
	if err != nil {
		return err
	}
	event.Action.ID = actionID
	if err := json.NewEncoder(stdout).Encode(event); err != nil {
		return fmt.Errorf("pitot: emit normalized event: %w", err)
	}
	if runtimePath == "" {
		return nil
	}
	client, err := runtime.OpenClient(runtimePath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return errBlocked
	}
	response, err := client.DeliverEvent(ctx, event)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return errBlocked
	}
	if response == nil || response.Outcome == schema.OutcomeAllow {
		return nil
	}
	if response.Outcome != schema.OutcomeDeny || response.ActionID != actionID {
		fmt.Fprintln(stderr, "pitot: invalid controller resolution")
		return errBlocked
	}
	if response.Message != "" {
		fmt.Fprintln(stderr, response.Message)
	} else {
		fmt.Fprintln(stderr, "Pitot Controller denied the shell request")
	}
	return errBlocked
}

func runRequest(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("pitot: request requires a kind")
	}
	kind := args[0]
	runtimePath := ""
	data := json.RawMessage(`{}`)
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--runtime":
			if i+1 >= len(args) {
				return errors.New("pitot: --runtime requires a path")
			}
			runtimePath = args[i+1]
			i++
		case "--data":
			if i+1 >= len(args) {
				return errors.New("pitot: --data requires JSON")
			}
			data = json.RawMessage(args[i+1])
			i++
		default:
			return fmt.Errorf("pitot: unexpected request argument %q", args[i])
		}
	}
	if runtimePath == "" {
		runtimePath = os.Getenv("PITOT_RUNTIME")
	}
	if runtimePath == "" {
		return errors.New("pitot: request requires --runtime PATH or PITOT_RUNTIME")
	}
	if !json.Valid(data) {
		return errors.New("pitot: --data must be valid JSON")
	}
	actionID, err := runtime.NewActionID()
	if err != nil {
		return err
	}
	client, err := runtime.OpenClient(runtimePath)
	if err != nil {
		return err
	}
	response, err := client.Request(ctx, schema.ControlRequested{
		PitotVersion: schema.Version,
		Type:         schema.TypeControlRequested,
		Kind:         kind,
		ActionID:     actionID,
		Data:         data,
	})
	if err != nil {
		return err
	}
	if err := json.NewEncoder(stdout).Encode(response); err != nil {
		return err
	}
	if response.Outcome == schema.OutcomeDeny {
		return errBlocked
	}
	if response.Outcome != schema.OutcomeAllow {
		return errors.New("pitot: invalid controller outcome")
	}
	return nil
}

func doctor(stdout io.Writer) error {
	fmt.Fprintf(stdout, "Pitot %s — local boundary\n", schema.Version)
	fmt.Fprintf(stdout, "adapter version: %s\n", adapters.AdapterVersion)
	fmt.Fprintln(stdout, "unauthenticated local socket: none")
	fmt.Fprintln(stdout, "runtime capabilities: hook_control consumer_delivery explicit_request")
	fmt.Fprintln(stdout, "hosts:")
	for _, host := range adapters.Supported() {
		probe, err := adapters.CanonicalHookEvent(host)
		if err != nil {
			return err
		}
		status := "PASS"
		if _, err := sensor.Decode(host, probe, "sha256"); err != nil {
			status = "FAIL: " + err.Error()
		}
		fmt.Fprintf(stdout, "  %-7s events=%v decoder=%s\n", host, adapters.HookEvents(host), status)
	}
	return nil
}

func runRuntime(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	configPath := ""
	runtimePath := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			if i+1 >= len(args) {
				return errors.New("pitot: --config requires a path")
			}
			configPath = args[i+1]
			i++
		case "--runtime":
			if i+1 >= len(args) {
				return errors.New("pitot: --runtime requires a path")
			}
			runtimePath = args[i+1]
			i++
		default:
			return fmt.Errorf("pitot: unexpected argument %q", args[i])
		}
	}
	if configPath == "" {
		return errors.New("pitot: run requires --config PATH")
	}
	if runtimePath == "" {
		runtimePath = os.Getenv("PITOT_RUNTIME")
	}
	if runtimePath == "" {
		return errors.New("pitot: run requires --runtime PATH or PITOT_RUNTIME")
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		return err
	}
	manager, err := runtime.Start(ctx, loaded.Config, stderr)
	if err != nil {
		return err
	}
	defer manager.Close()
	return runtime.NewServer(manager, loaded.SHA256, runtimePath, stdout, stderr).Serve(ctx)
}

func parseRuntimeFlag(args []string, required bool) (string, error) {
	runtimePath := ""
	for i := 0; i < len(args); i++ {
		if args[i] != "--runtime" {
			return "", fmt.Errorf("pitot: unexpected hook argument %q", args[i])
		}
		if i+1 >= len(args) {
			return "", errors.New("pitot: --runtime requires a path")
		}
		runtimePath = args[i+1]
		i++
	}
	if runtimePath == "" {
		runtimePath = os.Getenv("PITOT_RUNTIME")
	}
	if required && runtimePath == "" {
		return "", errors.New("pitot: runtime is required")
	}
	return runtimePath, nil
}

func usage() string {
	return `pitot — the open sensor and control transport for coding-agent tooling

usage:
  pitot init [--language python|typescript|go|rust] [--role consumer|controller] [--dir PATH] [--force]
  pitot dev --host HOST --exec "CMD ARGS"
  pitot doctor
  pitot run --config PATH --runtime PATH
  pitot hook HOST [--runtime PATH]
  pitot request KIND [--data JSON] --runtime PATH
`
}

func usageError() error { return errors.New(usage()) }
