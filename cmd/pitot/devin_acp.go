package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/operatorstack/pitot/internal/devinacp"
)

var runDevinACP = devinacp.Run

func runACP(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "devin" {
		return errors.New("pitot acp: requires host devin")
	}
	options := devinacp.Options{Executable: "devin", Cwd: ".", Stdout: stdout, Stderr: stderr}
	options.Runtime = os.Getenv("PITOT_RUNTIME")
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--runtime":
			value, next, err := flagValue(args, i, "--runtime")
			if err != nil {
				return err
			}
			options.Runtime, i = value, next
		case "--prompt":
			value, next, err := flagValue(args, i, "--prompt")
			if err != nil {
				return err
			}
			options.Prompt, i = value, next
		case "--exec":
			value, next, err := flagValue(args, i, "--exec")
			if err != nil {
				return err
			}
			options.Executable, i = value, next
		case "--model":
			value, next, err := flagValue(args, i, "--model")
			if err != nil {
				return err
			}
			options.Model, i = value, next
		case "--agent-type":
			value, next, err := flagValue(args, i, "--agent-type")
			if err != nil {
				return err
			}
			options.AgentType, i = value, next
		case "--cwd":
			value, next, err := flagValue(args, i, "--cwd")
			if err != nil {
				return err
			}
			options.Cwd, i = value, next
		default:
			return fmt.Errorf("pitot acp: unsupported argument %q (single-prompt Devin ACP supports --runtime, --prompt, --exec, --model, --agent-type, --cwd)", args[i])
		}
	}
	if options.Runtime == "" {
		return errors.New("pitot acp: --runtime is required (or set PITOT_RUNTIME)")
	}
	return runDevinACP(ctx, options)
}

func flagValue(args []string, index int, name string) (string, int, error) {
	if index+1 >= len(args) || args[index+1] == "" {
		return "", index, fmt.Errorf("pitot acp: %s requires a value", name)
	}
	return args[index+1], index + 1, nil
}

func devinOptionsFromAgent(program string, args []string, runtimePath string, stdout, stderr io.Writer) (devinacp.Options, error) {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(program)), ".exe")
	if name != "devin" {
		return devinacp.Options{}, fmt.Errorf("pitot dev: host devin requires a Devin executable, got %q", program)
	}
	options := devinacp.Options{
		Executable: program,
		Cwd:        ".",
		Runtime:    runtimePath,
		Stdout:     stdout,
		Stderr:     stderr,
	}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-p" || args[i] == "--print":
			if i+1 >= len(args) {
				return devinacp.Options{}, errors.New("pitot dev: Devin -p/--print requires a prompt")
			}
			options.Prompt = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--print="):
			options.Prompt = strings.TrimPrefix(args[i], "--print=")
		case args[i] == "--model":
			if i+1 >= len(args) {
				return devinacp.Options{}, errors.New("pitot dev: Devin --model requires a value")
			}
			options.Model = args[i+1]
			i++
		default:
			return devinacp.Options{}, fmt.Errorf("pitot dev: unsupported Devin argument %q; initial ACP support accepts -p/--print and --model", args[i])
		}
	}
	if options.Prompt == "" {
		return devinacp.Options{}, errors.New("pitot dev: Devin ACP support requires one -p/--print prompt")
	}
	return options, nil
}
