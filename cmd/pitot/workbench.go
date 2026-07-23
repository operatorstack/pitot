package main

import (
	"bufio"
	"context"
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
	"github.com/operatorstack/pitot/runtime"
)

// pitotPackageVersion is the published SDK version wired into generated
// manifests so a freshly initialized project can resolve its dependency.
const pitotPackageVersion = "0.1.0"

// pitotPythonDistribution is the PyPI distribution name for the Python SDK. The
// bare name "pitot" is already taken on PyPI by an unrelated aeronautics
// project, so the distribution is published as operatorstack-pitot while the
// importable package remains "pitot".
const pitotPythonDistribution = "operatorstack-pitot"

var supportedLanguages = []string{"python", "typescript", "go", "rust"}
var supportedRoles = []string{"consumer", "controller"}

// supportedTemplates enumerates the project scaffolds pitot init can emit.
// shell-policy is the only one that registers a controller for the shell action
// kind; the others target the test.approval kind or the consumer role.
var supportedTemplates = []string{"shell-policy", "release-approval", "blank-controller", "blank-consumer"}

// runInit scaffolds a complete, runnable Pitot project. It validates its inputs,
// detects or interactively selects the language and role, refuses to overwrite
// existing files unless --force is set, and writes a package manifest alongside
// the source so the generated project builds and runs without further setup.
func runInit(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	lang := ""
	role := ""
	template := ""
	dir := "pitot-project"
	force := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--language":
			if i+1 >= len(args) {
				return errors.New("pitot init: --language requires a value (python, typescript, go, rust)")
			}
			lang = args[i+1]
			i++
		case "--role":
			if i+1 >= len(args) {
				return errors.New("pitot init: --role requires a value (consumer, controller)")
			}
			role = args[i+1]
			i++
		case "--template":
			if i+1 >= len(args) {
				return fmt.Errorf("pitot init: --template requires a value (%s)", strings.Join(supportedTemplates, ", "))
			}
			template = args[i+1]
			i++
		case "--dir":
			if i+1 >= len(args) {
				return errors.New("pitot init: --dir requires a path")
			}
			dir = args[i+1]
			i++
		case "--force":
			force = true
		default:
			return fmt.Errorf("pitot init: unexpected argument %q", args[i])
		}
	}

	interactive := isInteractive(stdin)
	reader := bufio.NewReader(stdin)

	// Resolve language: explicit flag, then detection, then prompt.
	if lang == "" {
		if detected := detectLanguage(dir); detected != "" {
			fmt.Fprintf(stdout, "Detected %s project in %s\n", detected, dir)
			lang = detected
		} else if interactive {
			choice, err := promptChoice(reader, stdout, "Select a language", supportedLanguages, "python")
			if err != nil {
				return err
			}
			lang = choice
		} else {
			return errors.New("pitot init: --language is required when it cannot be detected and no terminal is attached")
		}
	}
	if !contains(supportedLanguages, lang) {
		return fmt.Errorf("pitot init: unsupported language %q (want python, typescript, go, rust)", lang)
	}

	// An explicit template implies its role, so we never prompt for a role the
	// template already dictates.
	if role == "" && template != "" && contains(supportedTemplates, template) {
		role = templateRole(template)
	}

	// Resolve role: explicit flag, then prompt, then default.
	if role == "" {
		if interactive {
			choice, err := promptChoice(reader, stdout, "Select a role", supportedRoles, "controller")
			if err != nil {
				return err
			}
			role = choice
		} else {
			role = "controller"
		}
	}
	if !contains(supportedRoles, role) {
		return fmt.Errorf("pitot init: unsupported role %q (want consumer, controller)", role)
	}

	// Resolve template: explicit flag validated for consistency with the role,
	// otherwise defaulted from the role so existing (role-only) callers keep the
	// blank scaffolds they got before templates existed.
	template, err := resolveTemplate(role, template)
	if err != nil {
		return err
	}

	files, err := projectFiles(lang, template)
	if err != nil {
		return err
	}

	// Non-destructive: refuse to clobber existing files unless --force.
	if !force {
		var conflicts []string
		for name := range files {
			if _, statErr := os.Stat(filepath.Join(dir, name)); statErr == nil {
				conflicts = append(conflicts, name)
			}
		}
		if len(conflicts) > 0 {
			return fmt.Errorf("pitot init: refusing to overwrite existing files in %s: %s (use --force)", dir, strings.Join(sorted(conflicts), ", "))
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("pitot init: create project directory: %w", err)
	}
	for _, name := range sorted(keys(files)) {
		path := filepath.Join(dir, name)
		if parent := filepath.Dir(path); parent != dir {
			if err := os.MkdirAll(parent, 0o755); err != nil {
				return fmt.Errorf("pitot init: create %s: %w", parent, err)
			}
		}
		if err := os.WriteFile(path, []byte(files[name]), 0o644); err != nil {
			return fmt.Errorf("pitot init: write %s: %w", name, err)
		}
	}

	fmt.Fprintf(stdout, "Initialized %s %s (%s) in %s\n", lang, role, template, dir)
	fmt.Fprintf(stdout, "Files written: %s\n", strings.Join(sorted(keys(files)), ", "))
	// The runtime launches the generated program from .pitot.yaml; the command
	// after `--` is the coding agent Pitot supervises, never the Controller.
	fmt.Fprintln(stdout, "Next:")
	fmt.Fprintf(stdout, "  1. cd %s\n", dir)
	fmt.Fprintln(stdout, "  2. Configure a supported host hook (see: pitot doctor --host HOST).")
	fmt.Fprintln(stdout, "  3. Run: pitot dev --host HOST -- AGENT [ARGS...]")
	fmt.Fprintln(stdout, "     example: pitot dev --host kimi -- kimi -p \"<prompt>\"")
	return nil
}

// resolveTemplate reconciles the explicit --template value with the resolved
// role. An empty template defaults from the role (controller -> blank-controller,
// consumer -> blank-consumer); an explicit template must not contradict an
// explicit role.
func resolveTemplate(role, template string) (string, error) {
	if template == "" {
		if role == "consumer" {
			return "blank-consumer", nil
		}
		return "blank-controller", nil
	}
	if !contains(supportedTemplates, template) {
		return "", fmt.Errorf("pitot init: unsupported template %q (want %s)", template, strings.Join(supportedTemplates, ", "))
	}
	if templateRole(template) != role {
		return "", fmt.Errorf("pitot init: template %q implies role %q but role %q was requested", template, templateRole(template), role)
	}
	return template, nil
}

// templateRole reports the role a template scaffolds.
func templateRole(template string) string {
	if template == "blank-consumer" {
		return "consumer"
	}
	return "controller"
}

// controllerKind returns the .pitot.yaml request kind a controller template
// registers under. Only shell-policy governs the shell action kind that host
// adapters (Kimi, Claude, Codex, ...) normalize Bash/PreToolUse events into.
func controllerKind(template string) string {
	if template == "shell-policy" {
		return "shell"
	}
	return "test.approval"
}

// controllerID returns the controller id embedded in the generated config and
// source for a template.
func controllerID(template string) string {
	if template == "shell-policy" {
		return "local-shell-policy"
	}
	return "local-controller"
}

// projectFiles returns the complete file set for a language/template: source,
// package manifest(s), and the .pitot.yaml runtime configuration.
func projectFiles(lang, template string) (map[string]string, error) {
	files := map[string]string{}

	src, err := sourceTemplate(lang, template)
	if err != nil {
		return nil, err
	}
	switch lang {
	case "python":
		files["main.py"] = src
		files["requirements.txt"] = fmt.Sprintf("%s>=%s\n", pitotPythonDistribution, pitotPackageVersion)
		files["pyproject.toml"] = pythonProjectManifest
	case "typescript":
		files["main.ts"] = src
		files["package.json"] = tsProjectManifest
		files["tsconfig.json"] = tsProjectTSConfig
	case "go":
		files["main.go"] = src
		files["go.mod"] = goProjectManifest
	case "rust":
		files["main.rs"] = src
		files["Cargo.toml"] = rustProjectManifest
	default:
		return nil, fmt.Errorf("pitot init: unsupported language %q", lang)
	}

	files[".pitot.yaml"] = pitotConfig(lang, template)
	return files, nil
}

// sourceTemplate selects the program source for a language/template pair.
func sourceTemplate(lang, template string) (string, error) {
	shellPolicy := template == "shell-policy"
	consumer := template == "blank-consumer"
	switch lang {
	case "python":
		switch {
		case consumer:
			return pythonConsumerTemplate, nil
		case shellPolicy:
			return pythonShellPolicyTemplate, nil
		default:
			return pythonControllerTemplate, nil
		}
	case "typescript":
		switch {
		case consumer:
			return tsConsumerTemplate, nil
		case shellPolicy:
			return tsShellPolicyTemplate, nil
		default:
			return tsControllerTemplate, nil
		}
	case "go":
		switch {
		case consumer:
			return goConsumerTemplate, nil
		case shellPolicy:
			return goShellPolicyTemplate, nil
		default:
			return goControllerTemplate, nil
		}
	case "rust":
		switch {
		case consumer:
			return rustConsumerTemplate, nil
		case shellPolicy:
			return rustShellPolicyTemplate, nil
		default:
			return rustControllerTemplate, nil
		}
	default:
		return "", fmt.Errorf("pitot init: unsupported language %q", lang)
	}
}

// pitotConfig renders the .pitot.yaml wiring the generated template to its run
// command. Consumers subscribe to events; controllers register for a request
// kind — shell-policy under "shell" (the kind host adapters normalize Bash
// events into), every other controller under "test.approval".
func pitotConfig(lang, template string) string {
	cmdList := runCommandList(lang)
	if templateRole(template) == "consumer" {
		return `consumers:
  - id: local-consumer
    command: ` + cmdList + `
    events: ["action.requested"]
    projection:
      content: full
`
	}
	return fmt.Sprintf(`controllers:
  %s:
    id: %s
    command: %s
    deadline_ms: 2000
    on_timeout: deny
    on_unavailable: deny
`, controllerKind(template), controllerID(template), cmdList)
}

// runCommandList is the JSON array form embedded in .pitot.yaml.
func runCommandList(lang string) string {
	switch lang {
	case "python":
		return `["python3", "main.py"]`
	case "typescript":
		return `["npx", "tsx", "main.ts"]`
	case "go":
		return `["go", "run", "main.go"]`
	case "rust":
		return `["cargo", "run", "--quiet"]`
	default:
		return `[]`
	}
}

// detectLanguage inspects an existing directory for a language's marker manifest.
func detectLanguage(dir string) string {
	markers := []struct {
		file string
		lang string
	}{
		{"go.mod", "go"},
		{"Cargo.toml", "rust"},
		{"package.json", "typescript"},
		{"tsconfig.json", "typescript"},
		{"pyproject.toml", "python"},
		{"requirements.txt", "python"},
	}
	for _, m := range markers {
		if _, err := os.Stat(filepath.Join(dir, m.file)); err == nil {
			return m.lang
		}
	}
	return ""
}

// isInteractive reports whether r is a terminal we can prompt on.
func isInteractive(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// promptChoice asks the user to pick from options, returning def on empty input.
func promptChoice(reader *bufio.Reader, stdout io.Writer, question string, options []string, def string) (string, error) {
	fmt.Fprintf(stdout, "%s [%s] (default %s): ", question, strings.Join(options, "/"), def)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("pitot init: read choice: %w", err)
	}
	choice := strings.TrimSpace(line)
	if choice == "" {
		return def, nil
	}
	if !contains(options, choice) {
		return "", fmt.Errorf("pitot init: %q is not one of %s", choice, strings.Join(options, ", "))
	}
	return choice, nil
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sorted(items []string) []string {
	out := append([]string(nil), items...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

const pythonControllerTemplate = `import sys
from pitot.runner import run_controller, allow
from pitot.types import ControlRequested

def handler(req: ControlRequested):
    return allow("Action approved by Python Controller")

if __name__ == "__main__":
    run_controller("local-controller", handler)
`

const pythonConsumerTemplate = `import sys
from pitot.runner import run_consumer
from pitot.types import Event

def handler(event: Event):
    print(f"Consumed event: {event.type}", file=sys.stderr)

if __name__ == "__main__":
    run_consumer(handler)
`

const pythonProjectManifest = `[build-system]
requires = ["setuptools>=61.0"]
build-backend = "setuptools.build_meta"

[project]
name = "pitot-project"
version = "0.1.0"
requires-python = ">=3.10"
# The PyPI distribution is operatorstack-pitot; the import package is "pitot".
dependencies = ["operatorstack-pitot>=0.1.0"]
`

const tsControllerTemplate = `import { runController, allow, ControlRequested } from '@operatorstack/pitot';

runController("local-controller", async (req: ControlRequested) => {
    return allow("Action approved by TS Controller");
});
`

const tsConsumerTemplate = `import { runConsumer, Event } from '@operatorstack/pitot';

runConsumer(async (event: Event) => {
    console.error("Consumed event:", event.type);
});
`

const tsProjectManifest = `{
  "name": "pitot-project",
  "version": "0.1.0",
  "private": true,
  "scripts": {
    "dev": "tsx main.ts"
  },
  "dependencies": {
    "@operatorstack/pitot": "^0.1.0"
  },
  "devDependencies": {
    "tsx": "^4.7.0",
    "typescript": "^5.0.0"
  }
}
`

const tsProjectTSConfig = `{
  "compilerOptions": {
    "target": "es2022",
    "module": "commonjs",
    "moduleResolution": "node",
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": true
  },
  "include": ["main.ts"]
}
`

const goControllerTemplate = `package main

import (
	"github.com/operatorstack/pitot/schema"
	"github.com/operatorstack/pitot/sdk"
)

func main() {
	sdk.RunController("local-controller", func(req schema.ControlRequested) sdk.Outcome {
		return sdk.Allow("Action approved by Go Controller")
	})
}
`

const goConsumerTemplate = `package main

import (
	"fmt"
	"os"

	"github.com/operatorstack/pitot/schema"
	"github.com/operatorstack/pitot/sdk"
)

func main() {
	sdk.RunConsumer(func(event schema.Event) {
		fmt.Fprintf(os.Stderr, "Consumed event: %s\n", event.Type)
	})
}
`

const goProjectManifest = `module pitot-project

go 1.22

require github.com/operatorstack/pitot v0.1.0
`

const rustControllerTemplate = `use pitot::{run_controller, allow, ControlRequested, Outcome};

fn handler(_req: ControlRequested) -> Outcome {
    allow(Some("Action approved by Rust Controller".to_string()))
}

fn main() {
    run_controller("local-controller", Box::new(handler));
}
`

const rustConsumerTemplate = `use pitot::{run_consumer, Event};

fn handler(event: Event) {
    eprintln!("Consumed event: {}", event.type_field);
}

fn main() {
    run_consumer(Box::new(handler));
}
`

const rustProjectManifest = `[package]
name = "pitot-project"
version = "0.1.0"
edition = "2021"

[[bin]]
name = "pitot-project"
path = "main.rs"

[dependencies]
pitot = "0.1.0"
serde_json = "1"
`

// The shell-policy templates below decode the Pitot Event carried in the control
// request, require full content projection, extract the normalized shell command,
// and deny only the PITOT_DENY_ME canary. The substring check is a sample
// tripwire, NOT production-grade shell security.

const goShellPolicyTemplate = `package main

import (
	"encoding/json"
	"strings"

	"github.com/operatorstack/pitot/schema"
	"github.com/operatorstack/pitot/sdk"
)

func main() {
	sdk.RunController("local-shell-policy", func(req schema.ControlRequested) sdk.Outcome {
		var event schema.Event
		if err := json.Unmarshal(req.Data, &event); err != nil {
			return sdk.Deny("Pitot sample policy could not decode the event.")
		}
		if event.Content == nil || event.Content.Mode != schema.ContentFull {
			return sdk.Deny("Pitot sample policy requires full content mode.")
		}
		var command string
		if err := json.Unmarshal(event.Content.Full, &command); err != nil {
			return sdk.Deny("Pitot sample policy could not decode the command.")
		}
		// Sample tripwire only — not a general shell-security control.
		if strings.Contains(command, "PITOT_DENY_ME") {
			return sdk.Deny("Pitot sample policy blocked the PITOT_DENY_ME canary.")
		}
		return sdk.Allow("Pitot sample policy allowed the shell request.")
	})
}
`

const pythonShellPolicyTemplate = `from pitot.runner import run_controller, allow, deny
from pitot.types import ControlRequested

def handler(req: ControlRequested):
    event = req.data or {}
    content = event.get("content") or {}
    if content.get("mode") != "full":
        return deny("Pitot sample policy requires full content mode.")
    command = content.get("full") or ""
    # Sample tripwire only — not a general shell-security control.
    if "PITOT_DENY_ME" in command:
        return deny("Pitot sample policy blocked the PITOT_DENY_ME canary.")
    return allow("Pitot sample policy allowed the shell request.")

if __name__ == "__main__":
    run_controller("local-shell-policy", handler)
`

const tsShellPolicyTemplate = `import { runController, allow, deny, ControlRequested, Event } from '@operatorstack/pitot';

runController("local-shell-policy", async (req: ControlRequested) => {
    const event = (req.data ?? {}) as Event;
    if (!event.content || event.content.mode !== "full") {
        return deny("Pitot sample policy requires full content mode.");
    }
    const command = String(event.content.full ?? "");
    // Sample tripwire only — not a general shell-security control.
    if (command.includes("PITOT_DENY_ME")) {
        return deny("Pitot sample policy blocked the PITOT_DENY_ME canary.");
    }
    return allow("Pitot sample policy allowed the shell request.");
});
`

const rustShellPolicyTemplate = `use pitot::{run_controller, allow, deny, ControlRequested, Event, Outcome};

fn handler(req: ControlRequested) -> Outcome {
    let event: Event = match req.data.and_then(|d| serde_json::from_value(d).ok()) {
        Some(e) => e,
        None => return deny(Some("Pitot sample policy could not decode the event.".to_string())),
    };
    let content = match event.content {
        Some(c) if c.mode == "full" => c,
        _ => return deny(Some("Pitot sample policy requires full content mode.".to_string())),
    };
    let command = content.full.and_then(|v| v.as_str().map(str::to_string)).unwrap_or_default();
    // Sample tripwire only — not a general shell-security control.
    if command.contains("PITOT_DENY_ME") {
        return deny(Some("Pitot sample policy blocked the PITOT_DENY_ME canary.".to_string()));
    }
    allow(Some("Pitot sample policy allowed the shell request.".to_string()))
}

fn main() {
    run_controller("local-shell-policy", Box::new(handler));
}
`

// runDev launches the runtime and a single agent against a chosen host, waits
// for the runtime to be ready before starting the agent, renders a decision
// timeline, and cleans up its private runtime descriptor on exit.
func runDev(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	host := ""
	execCmd := ""
	var execArgs []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--host":
			if i+1 >= len(args) {
				return errors.New("pitot dev: --host requires a value")
			}
			host = args[i+1]
			i++
		case "--exec":
			if i+1 >= len(args) {
				return errors.New("pitot dev: --exec requires a value")
			}
			execCmd = args[i+1]
			i++
		case "--":
			// Everything after -- is the agent command and its arguments.
			rest := args[i+1:]
			if len(rest) > 0 {
				execCmd = rest[0]
				execArgs = rest[1:]
			}
			i = len(args)
		default:
			return fmt.Errorf("pitot dev: unexpected argument %q", args[i])
		}
	}

	if host == "" || execCmd == "" {
		return errors.New("pitot dev: requires both --host and --exec (or --exec/-- CMD ARGS)")
	}
	if !adapters.IsSupported(adapters.Host(host)) {
		return fmt.Errorf("pitot dev: unsupported host %q (want one of: %s)", host, hostList())
	}

	// If --exec carried a full command line, split it into program + args.
	// An explicit `-- CMD ARGS` form (execArgs already set) takes precedence.
	program := execCmd
	programArgs := execArgs
	if len(programArgs) == 0 {
		fields := strings.Fields(execCmd)
		if len(fields) == 0 {
			return errors.New("pitot dev: --exec is empty")
		}
		program = fields[0]
		programArgs = fields[1:]
	}

	configPath := ".pitot.yaml"
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return errors.New("pitot dev: .pitot.yaml not found in current directory. Run 'pitot init' first")
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("pitot dev: load config: %w", err)
	}

	// Private, per-invocation runtime directory so concurrent runs never collide.
	runtimeDir, err := os.MkdirTemp("", "pitot-dev-")
	if err != nil {
		return fmt.Errorf("pitot dev: create runtime dir: %w", err)
	}
	defer os.RemoveAll(runtimeDir)
	runtimePath := filepath.Join(runtimeDir, "runtime.json")

	fmt.Fprintf(stdout, "Starting Pitot dev environment for host %s...\n", host)

	// Cancelable context so we can reap the server goroutine on exit.
	devCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	manager, err := runtime.Start(devCtx, loaded.Config, stderr)
	if err != nil {
		return fmt.Errorf("pitot dev: start runtime: %w", err)
	}
	defer manager.Close()

	// Render a decision timeline as controllers resolve actions.
	manager.SetDecisionObserver(func(d runtime.Decision) {
		marker := "ALLOW"
		if d.Outcome != "allow" {
			marker = strings.ToUpper(d.Outcome)
		}
		if d.Message != "" {
			fmt.Fprintf(stdout, "  [%s] %s (%s) — %s\n", marker, d.Kind, d.ActionID, d.Message)
		} else {
			fmt.Fprintf(stdout, "  [%s] %s (%s)\n", marker, d.Kind, d.ActionID)
		}
	})

	srv := runtime.NewServer(manager, loaded.SHA256, runtimePath, stdout, stderr)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(devCtx) }()

	// Wait for the runtime to publish a usable descriptor before starting the agent.
	if err := waitForRuntime(devCtx, runtimePath, serveErr, 5*time.Second); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "Runtime ready. Starting agent: %s %s\n", program, strings.Join(programArgs, " "))
	fmt.Fprintln(stdout, "Decisions:")

	cmd := exec.CommandContext(devCtx, program, programArgs...)
	cmd.Env = append(os.Environ(), "PITOT_RUNTIME="+runtimePath)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = os.Stdin

	runErr := cmd.Run()

	// Stop the runtime and reap its goroutine before returning.
	cancel()
	select {
	case <-serveErr:
	case <-time.After(5 * time.Second):
	}

	if runErr != nil {
		return fmt.Errorf("pitot dev: agent execution failed: %w", runErr)
	}
	fmt.Fprintln(stdout, "Agent finished. Runtime stopped.")
	return nil
}

// waitForRuntime blocks until the runtime descriptor is live, the server errors,
// or the deadline elapses.
func waitForRuntime(ctx context.Context, runtimePath string, serveErr <-chan error, timeout time.Duration) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-serveErr:
			if err != nil {
				return fmt.Errorf("pitot dev: runtime server error: %w", err)
			}
			return errors.New("pitot dev: runtime server stopped before becoming ready")
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return errors.New("pitot dev: runtime did not become ready within timeout")
		case <-ticker.C:
			client, err := runtime.OpenClient(runtimePath)
			if err != nil {
				continue
			}
			if err := client.Health(ctx); err == nil {
				return nil
			}
		}
	}
}

func hostList() string {
	hosts := adapters.Supported()
	names := make([]string, len(hosts))
	for i, h := range hosts {
		names[i] = string(h)
	}
	return strings.Join(names, ", ")
}
