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

var supportedLanguages = []string{"python", "typescript", "go", "rust"}
var supportedRoles = []string{"consumer", "controller"}

// runInit scaffolds a complete, runnable Pitot project. It validates its inputs,
// detects or interactively selects the language and role, refuses to overwrite
// existing files unless --force is set, and writes a package manifest alongside
// the source so the generated project builds and runs without further setup.
func runInit(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	lang := ""
	role := ""
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

	files, err := projectFiles(lang, role)
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

	fmt.Fprintf(stdout, "Initialized %s %s in %s\n", lang, role, dir)
	fmt.Fprintf(stdout, "Files written: %s\n", strings.Join(sorted(keys(files)), ", "))
	fmt.Fprintf(stdout, "Next: cd %s && pitot dev --host claude --exec %q\n", dir, runCommandString(lang))
	return nil
}

// projectFiles returns the complete file set for a language/role: source,
// package manifest(s), and the .pitot.yaml runtime configuration.
func projectFiles(lang, role string) (map[string]string, error) {
	files := map[string]string{}

	controller := role == "controller"
	switch lang {
	case "python":
		if controller {
			files["main.py"] = pythonControllerTemplate
		} else {
			files["main.py"] = pythonConsumerTemplate
		}
		files["requirements.txt"] = fmt.Sprintf("pitot>=%s\n", pitotPackageVersion)
		files["pyproject.toml"] = pythonProjectManifest
	case "typescript":
		if controller {
			files["main.ts"] = tsControllerTemplate
		} else {
			files["main.ts"] = tsConsumerTemplate
		}
		files["package.json"] = tsProjectManifest
		files["tsconfig.json"] = tsProjectTSConfig
	case "go":
		if controller {
			files["main.go"] = goControllerTemplate
		} else {
			files["main.go"] = goConsumerTemplate
		}
		files["go.mod"] = goProjectManifest
	case "rust":
		if controller {
			files["main.rs"] = rustControllerTemplate
		} else {
			files["main.rs"] = rustConsumerTemplate
		}
		files["Cargo.toml"] = rustProjectManifest
	default:
		return nil, fmt.Errorf("pitot init: unsupported language %q", lang)
	}

	files[".pitot.yaml"] = pitotConfig(lang, role)
	return files, nil
}

// pitotConfig renders the .pitot.yaml wiring the generated role to its run command.
func pitotConfig(lang, role string) string {
	cmdList := runCommandList(lang)
	if role == "consumer" {
		return `consumers:
  - id: local-consumer
    command: ` + cmdList + `
    events: ["action.requested"]
    projection:
      content: full
`
	}
	return fmt.Sprintf(`controllers:
  test.approval:
    id: local-controller
    command: %s
    deadline_ms: 2000
    on_timeout: deny
    on_unavailable: deny
`, cmdList)
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

// runCommandString is the human-readable command shown in the init next-step hint.
func runCommandString(lang string) string {
	switch lang {
	case "python":
		return "python3 main.py"
	case "typescript":
		return "npx tsx main.ts"
	case "go":
		return "go run main.go"
	case "rust":
		return "cargo run --quiet"
	default:
		return ""
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
dependencies = ["pitot>=0.1.0"]
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
