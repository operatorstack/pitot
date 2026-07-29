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
		return doctor(args[1:], stdout, stderr)
	case "run":
		return runRuntime(ctx, args[1:], stdout, stderr)
	case "hook":
		return runHook(ctx, args[1:], stdin, stdout, stderr)
	case "request":
		return runRequest(ctx, args[1:], stdout)
	case "version", "--version", "-v":
		return runVersion(stdout)
	case "upgrade":
		return runUpgrade(ctx, args[1:], stdout, stderr)
	case "install":
		return runInstall(args[1:], stdout, stderr)
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

func doctor(args []string, stdout, stderr io.Writer) error {
	host := ""
	fix := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--host":
			if i+1 >= len(args) {
				return errors.New("pitot doctor: --host requires a value")
			}
			host = args[i+1]
			i++
		case "--fix":
			fix = true
		default:
			return fmt.Errorf("pitot doctor: unexpected argument %q", args[i])
		}
	}
	if fix && host == "" {
		return errors.New("pitot doctor: --fix requires --host")
	}
	if host != "" {
		if fix {
			// The only mutation doctor ever performs, and only on request:
			// restore Pitot's own marked entries (foreign config untouched).
			return runWireHost(host, true, stdout)
		}
		return doctorHost(adapters.Host(host), stdout, stderr)
	}

	fmt.Fprintf(stdout, "Pitot %s — local boundary\n", schema.Version)
	fmt.Fprintf(stdout, "binary version: %s (%s)\n", releaseVersion(), buildCommit())
	fmt.Fprintf(stdout, "adapter version: %s\n", adapters.AdapterVersion)
	fmt.Fprintln(stdout, "unauthenticated local socket: none")
	fmt.Fprintln(stdout, "runtime capabilities: hook_control consumer_delivery explicit_request")
	printHydrationStatus(stdout)
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
	if runtimePath == "" {
		runtimePath = os.Getenv("PITOT_RUNTIME")
	}
	if runtimePath == "" {
		return errors.New("pitot: run requires --runtime PATH or PITOT_RUNTIME")
	}
	var loaded config.Loaded
	var err error
	if configPath == "" {
		root, rootErr := config.FindRoot(".")
		if rootErr != nil {
			return errors.New("pitot: no config fragments under .pitot/conf.d (run 'pitot init' to register a controller or consumer, or pass --config PATH)")
		}
		loaded, err = config.Discover(root)
		if err == nil {
			anchorDirs(&loaded.Config, root)
		}
	} else {
		loaded, err = config.Load(configPath)
	}
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
  pitot init [--language python|typescript|go|rust] [--role consumer|controller] [--template shell-policy|release-approval|blank-controller|blank-consumer] [--dir PATH] [--fragment NAME] [--force]
  pitot init --host HOST [--force]
  pitot dev --host HOST -- AGENT [ARGS...]
  pitot doctor [--host HOST] [--fix]
  pitot run [--config PATH] --runtime PATH
  pitot hook HOST [--runtime PATH]
  pitot request KIND [--data JSON] --runtime PATH
  pitot version
  pitot upgrade [--to X.Y.Z] [--check] [--host HOST]
  pitot install typescript|python [--configure-only] [--revert] [--host HOST]

configuration is tenant-partitioned: each tool or user registers its processes
in its own fragment under .pitot/conf.d/; the runtime merges every fragment and
rejects collisions. --config PATH overrides discovery with one explicit file.

the binary is repo-pinned: .pitot/version names the exact release and the
committed shim .pitot/bin/pitot hydrates it on demand (sha256-verified, cached
per user). upgrade rewrites the pin only — commit that diff to upgrade every
clone. install wires typed SDKs from our registry through the front door.
`
}

func usageError() error { return errors.New(usage()) }
