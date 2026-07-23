package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/operatorstack/pitot/adapters"
)

// hostProbe describes what `pitot doctor --host` inspects for a coding agent:
// the agent binary, where its config lives, and the hook it must declare so its
// native blocking boundary reaches `pitot hook <host>`.
type hostProbe struct {
	binary     string
	configPath func() (string, error)
	hookEvent  string
	matcher    string
}

// hostProbes holds per-host inspection metadata. Only Kimi is wired for this
// release; other hosts fall back to a decoder-only note.
var hostProbes = map[adapters.Host]hostProbe{
	adapters.Kimi: {
		binary: "kimi",
		configPath: func() (string, error) {
			if home := os.Getenv("KIMI_CODE_HOME"); home != "" {
				return filepath.Join(home, "config.toml"), nil
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			return filepath.Join(home, ".kimi-code", "config.toml"), nil
		},
		hookEvent: "PreToolUse",
		matcher:   "Bash",
	},
}

// doctorHost reports whether a host is configured to route its blocking shell
// boundary to Pitot. It never edits configuration — it only inspects and
// reports, returning a non-nil error when a blocking issue is found so callers
// (and CI) get a clear signal.
func doctorHost(host adapters.Host, stdout, stderr io.Writer) error {
	if !adapters.IsSupported(host) {
		return fmt.Errorf("pitot doctor: unsupported host %q (want one of: %s)", host, hostList())
	}
	fmt.Fprintf(stdout, "Pitot %s — host check: %s\n", adapters.AdapterVersion, host)

	probe, known := hostProbes[host]
	if !known {
		fmt.Fprintf(stdout, "  host-config inspection is not implemented for %q in this release; run `pitot doctor` for the decoder status\n", host)
		return nil
	}

	var problems []string

	// 1. Agent binary on PATH.
	if path, err := exec.LookPath(probe.binary); err == nil {
		fmt.Fprintf(stdout, "  binary on PATH: %s\n", path)
	} else {
		fmt.Fprintf(stdout, "  binary on PATH: NOT FOUND (%s)\n", probe.binary)
		problems = append(problems, fmt.Sprintf("%q not on PATH", probe.binary))
	}

	// 2. Config path resolvable.
	cfgPath, err := probe.configPath()
	if err != nil {
		fmt.Fprintf(stdout, "  config path: UNRESOLVED (%v)\n", err)
		problems = append(problems, "config path unresolved")
	} else {
		fmt.Fprintf(stdout, "  config path: %s\n", cfgPath)
	}

	// 3. Hook present and invoking `pitot hook <host>`.
	hookCmd := "hook " + string(host)
	if cfgPath != "" {
		data, readErr := os.ReadFile(cfgPath)
		switch {
		case readErr != nil:
			fmt.Fprintf(stdout, "  config file: ABSENT (%v)\n", readErr)
			problems = append(problems, "config file absent")
		default:
			text := string(data)
			hasEvent := strings.Contains(text, `event = "`+probe.hookEvent+`"`)
			hasHookCmd := strings.Contains(text, "pitot "+hookCmd) || strings.Contains(text, hookCmd)
			if hasEvent && hasHookCmd {
				fmt.Fprintf(stdout, "  %s hook: FOUND invoking `pitot %s`\n", probe.hookEvent, hookCmd)
			} else {
				fmt.Fprintf(stdout, "  %s hook: MISSING — add a [[hooks]] entry (event = %q, matcher = %q, command = \"pitot %s\")\n",
					probe.hookEvent, probe.hookEvent, probe.matcher, hookCmd)
				problems = append(problems, "hook not configured")
			}
		}
	}

	// 4. Config parses — best effort via the agent's own doctor, if present.
	if _, err := exec.LookPath(probe.binary); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, derr := exec.CommandContext(ctx, probe.binary, "doctor").CombinedOutput()
		if derr == nil {
			fmt.Fprintf(stdout, "  config parses: PASS (%s doctor)\n", probe.binary)
		} else {
			fmt.Fprintf(stdout, "  config parses: FAIL (%s doctor: %v)\n", probe.binary, derr)
			if len(out) > 0 {
				fmt.Fprintf(stderr, "  %s doctor: %s\n", probe.binary, strings.TrimSpace(string(out)))
			}
			problems = append(problems, "config did not parse")
		}
	} else {
		fmt.Fprintf(stdout, "  config parses: skipped (%s not available)\n", probe.binary)
	}

	// Kimi's PreToolUse hook is fail-open on crash/timeout per host semantics.
	fmt.Fprintf(stdout, "  note: %s hooks are fail-open on hook crash or timeout per host semantics; this sample controller is not a security sandbox\n", host)

	if len(problems) > 0 {
		return fmt.Errorf("pitot doctor: %s host check found %d issue(s): %s", host, len(problems), strings.Join(problems, "; "))
	}
	fmt.Fprintf(stdout, "  ready: %s can route its shell boundary to Pitot\n", host)
	return nil
}
