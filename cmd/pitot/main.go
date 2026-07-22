// Command pitot is the reference Go executable for the Pitot sensor and control
// transport. In v1 Pitot supervises local processes: it starts declared
// Consumers and Controllers itself, projects content before bytes enter a child
// pipe, and exposes no unauthenticated local socket.
//
// This skeleton implements the `doctor` boundary inspection and the `run`
// configuration boundary; supervised delivery lands with the first buildable
// release.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/operatorstack/pitot/adapters"
	"github.com/operatorstack/pitot/schema"
	"github.com/operatorstack/pitot/sensor"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if err.Error() == "pitot: block" {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "doctor":
		return doctor(stdout)
	case "run":
		return runSupervisor(args[1:], stdout)
	case "hook":
		return runHook(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage())
		return nil
	default:
		fmt.Fprintln(stderr, usage())
		return fmt.Errorf("pitot: unknown command %q", args[0])
	}
}

// runHook implements the direct host CLI hook interface. It reads the raw hook payload
// from stdin, normalizes it, and exits with 0 (allow) or 2 (block/deny).
func runHook(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("pitot: hook requires a host identifier (claude, cline, codex, copilot, cursor, gemini, kimi, opencode, pi, qwen)")
	}
	host := adapters.Host(args[0])
	if !adapters.IsSupported(host) {
		return fmt.Errorf("pitot: unsupported hook host %q", host)
	}

	// Read raw payload from stdin
	payload, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("pitot: read stdin: %w", err)
	}

	// In this reference hook implementation, we decode with "full" projection
	event, err := sensor.Decode(host, payload, "full")
	if err != nil {
		// Serialize content-safe boundary fault to stderr
		if fault, ok := sensor.AsFault(err, "act_hook"); ok {
			_ = json.NewEncoder(stderr).Encode(fault)
		} else {
			fmt.Fprintln(stderr, err.Error())
		}
		// Return specific error to trigger exit code 2 in main()
		return fmt.Errorf("pitot: block")
	}

	// Print the normalized event to stdout (useful for logging/consumers)
	_ = json.NewEncoder(stdout).Encode(event)
	return nil
}

// doctor inspects the effective local boundary and proves the decoder against
// each host's canonical read-only probe, mirroring Boatstack's DiagnoseHook.
func doctor(stdout io.Writer) error {
	fmt.Fprintf(stdout, "Pitot %s — local boundary\n", schema.Version)
	fmt.Fprintf(stdout, "adapter version: %s\n", adapters.AdapterVersion)
	fmt.Fprintln(stdout, "unauthenticated local socket: none")
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

// runSupervisor validates the configuration boundary. Actual supervised delivery
// is not enabled in this skeleton; it never opens a socket.
func runSupervisor(args []string, stdout io.Writer) error {
	config := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			if i+1 >= len(args) {
				return fmt.Errorf("pitot: --config requires a path")
			}
			config = args[i+1]
			i++
		default:
			return fmt.Errorf("pitot: unexpected argument %q", args[i])
		}
	}
	if config == "" {
		return fmt.Errorf("pitot: run requires --config <path>")
	}
	if _, err := os.Stat(config); err != nil {
		return fmt.Errorf("pitot: cannot read config %q: %w", config, err)
	}
	fmt.Fprintf(stdout, "Pitot %s — configuration boundary\n", schema.Version)
	fmt.Fprintf(stdout, "config: %s\n", config)
	fmt.Fprintln(stdout, "supervised delivery: not enabled in this build")
	return nil
}

func usage() string {
	return `pitot — the open sensor and control transport for coding-agent tooling

usage:
  pitot doctor              inspect the effective local boundary
  pitot run --config PATH   start Pitot with repository-owned configuration
  pitot hook HOST           direct integration interface for host CLI hook payloads (reads stdin)
`
}

func usageError() error {
	return fmt.Errorf("%s", usage())
}
