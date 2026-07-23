package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/operatorstack/pitot/config"
	"github.com/operatorstack/pitot/runtime"
)

func runInit(args []string, stdout, stderr io.Writer) error {
	lang := "python"
	role := "controller"
	dir := "pitot-project"

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
		default:
			return fmt.Errorf("pitot init: unexpected argument %q", args[i])
		}
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create project directory: %v", err)
	}

	switch lang {
	case "python":
		if role == "controller" {
			err := writeTemplate(filepath.Join(dir, "main.py"), pythonControllerTemplate)
			if err != nil {
				return err
			}
		} else {
			err := writeTemplate(filepath.Join(dir, "main.py"), pythonConsumerTemplate)
			if err != nil {
				return err
			}
		}
	case "typescript":
		if role == "controller" {
			err := writeTemplate(filepath.Join(dir, "main.ts"), tsControllerTemplate)
			if err != nil {
				return err
			}
		} else {
			err := writeTemplate(filepath.Join(dir, "main.ts"), tsConsumerTemplate)
			if err != nil {
				return err
			}
		}
	case "go":
		if role == "controller" {
			err := writeTemplate(filepath.Join(dir, "main.go"), goControllerTemplate)
			if err != nil {
				return err
			}
		} else {
			err := writeTemplate(filepath.Join(dir, "main.go"), goConsumerTemplate)
			if err != nil {
				return err
			}
		}
	case "rust":
		if role == "controller" {
			err := writeTemplate(filepath.Join(dir, "main.rs"), rustControllerTemplate)
			if err != nil {
				return err
			}
		} else {
			err := writeTemplate(filepath.Join(dir, "main.rs"), rustConsumerTemplate)
			if err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported language: %s", lang)
	}

	// Write a basic pitot configuration
	cfg := `controllers:
  test.approval:
    id: local-controller
    command: %s
    deadline_ms: 2000
    on_timeout: deny
    on_unavailable: deny
`
	var cmdList string
	switch lang {
	case "python":
		cmdList = `["python3", "main.py"]`
	case "typescript":
		cmdList = `["npx", "tsx", "main.ts"]`
	case "go":
		cmdList = `["go", "run", "main.go"]`
	case "rust":
		cmdList = `["cargo", "run"]`
	}

	if role == "consumer" {
		cfg = `consumers:
  - id: local-consumer
    command: ` + cmdList + `
    events: ["action.requested"]
    projection:
      content: full
`
	} else {
		cfg = fmt.Sprintf(cfg, cmdList)
	}

	if err := os.WriteFile(filepath.Join(dir, ".pitot.yaml"), []byte(cfg), 0644); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "Initialized %s %s in %s\n", lang, role, dir)
	return nil
}

func writeTemplate(path string, tmpl string) error {
	return os.WriteFile(path, []byte(tmpl), 0644)
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

const tsControllerTemplate = `import { runController, allow } from 'pitot/runner';
import { ControlRequested } from 'pitot/pitot';

runController("local-controller", async (req: ControlRequested) => {
    return allow("Action approved by TS Controller");
});
`

const tsConsumerTemplate = `import { runConsumer } from 'pitot/runner';
import { Event } from 'pitot/pitot';

runConsumer(async (event: Event) => {
    console.error("Consumed event:", event.type);
});
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

func runDev(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	host := ""
	execCmd := ""

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
		default:
			return fmt.Errorf("pitot dev: unexpected argument %q", args[i])
		}
	}

	if host == "" || execCmd == "" {
		return errors.New("pitot dev: requires both --host and --exec")
	}

	fmt.Fprintf(stdout, "Starting Pitot dev environment for host %s...\n", host)

	configPath := ".pitot.yaml"
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return errors.New("pitot dev: .pitot.yaml not found in current directory. Run 'pitot init' first")
	}

	loaded, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %v", err)
	}

	runtimePath := filepath.Join(os.TempDir(), "pitot-dev-runtime.json")
	
	manager, err := runtime.Start(ctx, loaded.Config, stderr)
	if err != nil {
		return fmt.Errorf("failed to start runtime: %v", err)
	}
	defer manager.Close()

	srv := runtime.NewServer(manager, loaded.SHA256, runtimePath, stdout, stderr)
	
	// Start the runtime server in a goroutine
	go func() {
		if err := srv.Serve(ctx); err != nil {
			fmt.Fprintf(stderr, "runtime server error: %v\n", err)
		}
	}()

	fmt.Fprintf(stdout, "Runtime listening. Starting agent %s...\n", execCmd)
	
	cmd := exec.CommandContext(ctx, execCmd)
	cmd.Env = append(os.Environ(), "PITOT_RUNTIME="+runtimePath)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = os.Stdin
	
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("agent execution failed: %v", err)
	}

	return nil
}
