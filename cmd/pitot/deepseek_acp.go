package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/operatorstack/pitot/internal/deepseekacp"
)

var runDeepSeek = deepseekacp.Run

// npm's Windows command shim needs a shell; resolve its installed JS entrypoint
// explicitly so paths and arguments never pass through cmd.exe interpolation.
func deepSeekCommand(program string, arguments []string) (string, []string, error) {
	if len(arguments) > 0 {
		name := strings.TrimSuffix(strings.ToLower(filepath.Base(program)), ".exe")
		if name != "node" || len(arguments) != 1 || !filepath.IsAbs(arguments[0]) || filepath.Ext(arguments[0]) != ".js" {
			return "", nil, errors.New("pitot: DeepSeek accepts a dsh executable or node with one absolute dsh entrypoint; extra profile flags are not allowed")
		}
	}
	resolved, err := exec.LookPath(program)
	if err != nil {
		return "", nil, err
	}
	if !strings.EqualFold(filepath.Ext(resolved), ".cmd") {
		return resolved, arguments, nil
	}
	entry := filepath.Join(filepath.Dir(resolved), "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")
	if filepath.Base(filepath.Dir(resolved)) == ".bin" {
		entry = filepath.Join(filepath.Dir(filepath.Dir(resolved)), "@deepseek-ai", "dsh", "lib", "bin.js")
	}
	if info, err := os.Stat(entry); err != nil || !info.Mode().IsRegular() {
		return "", nil, errors.New("pitot: cannot resolve the installed dsh JS entrypoint")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return "", nil, err
	}
	return node, append([]string{entry}, arguments...), nil
}

func runDeepSeekACP(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	options := deepseekacp.Options{Executable: "dsh", Cwd: ".", Stdout: stdout, Stderr: stderr, Runtime: os.Getenv("PITOT_RUNTIME")}
	for i := 0; i < len(args); i++ {
		value, next, err := flagValue(args, i, args[i])
		if err != nil {
			return err
		}
		switch args[i] {
		case "--runtime":
			options.Runtime = value
		case "--prompt":
			options.Prompt = value
		case "--exec":
			options.Executable = value
		case "--cwd":
			options.Cwd = value
		case "--model":
			options.Model = value
		case "--base-url":
			options.BaseURL = value
		case "--api-key-env":
			options.APIKeyEnv = value
		case "--session-dir":
			options.SessionDir = value
		default:
			return fmt.Errorf("pitot: unsupported DeepSeek argument %q", args[i])
		}
		i = next
	}
	if options.Runtime == "" || options.Prompt == "" {
		return errors.New("pitot acp deepseek requires --runtime and --prompt")
	}
	return launchDeepSeek(ctx, options)
}

func launchDeepSeek(ctx context.Context, options deepseekacp.Options) error {
	program, arguments, err := deepSeekCommand(options.Executable, options.Arguments)
	if err != nil {
		return err
	}
	if err := checkDeepSeekCommandVersion(ctx, program, arguments); err != nil {
		return err
	}
	options.Executable, options.Arguments = program, arguments
	options.Pitot, err = os.Executable()
	if err != nil {
		return err
	}
	if override := os.Getenv("PITOT_BIN"); override != "" {
		if !filepath.IsAbs(override) {
			return errors.New("pitot: PITOT_BIN must be an absolute executable path")
		}
		options.Pitot = override
	}
	return runDeepSeek(ctx, options)
}
