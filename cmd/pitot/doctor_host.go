package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/operatorstack/pitot/adapters"
	"github.com/operatorstack/pitot/config"
	"github.com/operatorstack/pitot/wiring"
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

var doctorLookPath = exec.LookPath
var doctorCommand = exec.Command

// doctorHost reports whether a host is configured to route its blocking shell
// boundary to Pitot. It never edits configuration without an explicit --fix —
// plain doctor only inspects and reports, returning a non-nil error when a
// blocking issue is found so callers (and CI) get a clear signal.
func doctorHost(host adapters.Host, stdout, stderr io.Writer) error {
	if !adapters.IsSupported(host) {
		return fmt.Errorf("pitot doctor: unsupported host %q (want one of: %s)", host, hostList())
	}
	fmt.Fprintf(stdout, "Pitot %s — host check: %s\n", adapters.AdapterVersion, host)
	if host == adapters.Devin {
		return doctorDevin(stdout, stderr)
	}

	// Repo-wireable hosts report their wiring state against the fragment
	// witness (drift-is-named-not-silently-fixed).
	if wiring.Supported(string(host)) {
		root := "."
		if found, err := config.FindRoot("."); err == nil {
			root = found
		}
		err := printWiringStatus(root, string(host), stdout)
		doctorCommonNotes(host, stdout)
		return err
	}

	probe, known := hostProbes[host]
	if !known {
		fmt.Fprintf(stdout, "  host-config inspection is not implemented for %q in this release; run `pitot doctor` for the decoder status, or `pitot init --host %s` for the wiring snippet\n", host, host)
		doctorCommonNotes(host, stdout)
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
	doctorCommonNotes(host, stdout)

	if len(problems) > 0 {
		return fmt.Errorf("pitot doctor: %s host check found %d issue(s): %s", host, len(problems), strings.Join(problems, "; "))
	}
	fmt.Fprintf(stdout, "  ready: %s can route its shell boundary to Pitot\n", host)
	return nil
}

// doctorCommonNotes surfaces the two silent degradations every wired host is
// exposed to: hooks without a selected runtime observe instead of supervise,
// and user-level wrappers resolve `pitot` from PATH, which can drift from the
// binary the operator reviewed. Notes never fail the check; they name the
// state so the operator can decide.
func doctorCommonNotes(host adapters.Host, stdout io.Writer) {
	if os.Getenv("PITOT_RUNTIME") == "" {
		fmt.Fprintln(stdout, "  note: PITOT_RUNTIME is not set in this shell; hooks run observe-only until a runtime is selected (`pitot run` / `pitot dev`), and `pitot run --strict` or `require_controller: true` faults instead of allowing")
	}
	if wiring.Supported(string(host)) {
		return // repo-wired hosts pin .pitot/bin/pitot; PATH is not consulted
	}
	self, selfErr := os.Executable()
	onPath, pathErr := doctorLookPath("pitot")
	if selfErr != nil || pathErr != nil {
		return
	}
	selfSum, err1 := fileSHA256(self)
	pathSum, err2 := fileSHA256(onPath)
	if err1 != nil || err2 != nil {
		return
	}
	if selfSum != pathSum {
		fmt.Fprintf(stdout, "  warning: `pitot` on PATH (%s) is a different binary than this one (%s); user-level %s wrappers resolve ${PITOT_BIN:-pitot} from PATH, so the drifted binary would do the supervising\n", onPath, self, host)
	}
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func doctorDevin(stdout, stderr io.Writer) error {
	const supportedVersion = "3000.3.22"
	path, err := doctorLookPath("devin")
	if err != nil {
		fmt.Fprintln(stdout, "  binary on PATH: NOT FOUND (devin)")
		return errors.New("pitot doctor: devin host check found 1 issue: \"devin\" not on PATH")
	}
	fmt.Fprintf(stdout, "  binary on PATH: %s\n", path)
	version, versionErr := doctorCommand(path, "--version").CombinedOutput()
	if versionErr != nil {
		fmt.Fprintf(stderr, "  devin --version: %s\n", strings.TrimSpace(string(version)))
		return fmt.Errorf("pitot doctor: devin --version failed: %w", versionErr)
	}
	versionText := strings.TrimSpace(string(version))
	fmt.Fprintf(stdout, "  version: %s\n", versionText)
	if !strings.Contains(versionText, supportedVersion) {
		return fmt.Errorf("pitot doctor: Devin version %q is unsupported; install %s", versionText, supportedVersion)
	}
	help, helpErr := doctorCommand(path, "acp", "--help").CombinedOutput()
	if helpErr != nil || !strings.Contains(string(help), "Agent Client Protocol") {
		return errors.New("pitot doctor: installed Devin does not advertise the required ACP server")
	}
	fmt.Fprintln(stdout, "  ACP v1 boundary: FOUND (`devin acp`)")
	fmt.Fprintln(stdout, "  wiring: none required; use `pitot dev --host devin -- devin -p \"<prompt>\"`")
	fmt.Fprintln(stdout, "  ready: Devin can route its exec permission boundary to Pitot")
	return nil
}
