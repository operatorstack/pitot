package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/operatorstack/pitot/config"
	deepseek "github.com/operatorstack/pitot/integrations/deepseek"
	"github.com/operatorstack/pitot/internal/deepseekacp"
)

const deepSeekPluginPath = ".pitot/deepseek/pitot.mjs"

func deepSeekOwnedFiles() map[string][]byte {
	digest := sha256.Sum256([]byte(deepseek.Plugin))
	witness, _ := json.MarshalIndent(map[string]any{
		"schema_version": 1, "agent": "deepseek", "version": deepseek.Version,
		"plugin": "pitot.mjs", "plugin_sha256": hex.EncodeToString(digest[:]),
	}, "", "  ")
	return map[string][]byte{deepSeekPluginPath: []byte(deepseek.Plugin), ".pitot/deepseek/integration.json": append(witness, '\n')}
}

func checkDeepSeekPath(root, relative string) error {
	current := root
	for _, part := range strings.Split(filepath.FromSlash(relative), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("pitot: DeepSeek integration path is a symlink: %s", current)
		}
	}
	return nil
}

func writeDeepSeekIntegration(root string, force bool, stdout io.Writer) error {
	files := deepSeekOwnedFiles()
	for relative, expected := range files {
		if err := checkDeepSeekPath(root, relative); err != nil {
			return err
		}
		actual, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && !bytes.Equal(actual, expected) && !force {
			return fmt.Errorf("pitot: DeepSeek integration drifted at %s; inspect it before using --force", relative)
		}
	}
	for relative, body := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return err
		}
	}
	fmt.Fprintln(stdout, "Wired DeepSeek native shell control in .pitot/deepseek.")
	fmt.Fprintln(stdout, `Run: pitot dev --host deepseek --prompt "<prompt>" -- dsh`)
	return nil
}

func checkDeepSeekIntegration(root string) error {
	for relative, expected := range deepSeekOwnedFiles() {
		if err := checkDeepSeekPath(root, relative); err != nil {
			return err
		}
		actual, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return fmt.Errorf("pitot: missing DeepSeek integration; run pitot init --host deepseek: %w", err)
		}
		if !bytes.Equal(actual, expected) {
			return fmt.Errorf("pitot: DeepSeek integration drifted at %s", relative)
		}
	}
	return nil
}

func checkDeepSeekVersion(ctx context.Context, executable string) error {
	program, arguments, err := deepSeekCommand(executable, nil)
	if err != nil {
		return err
	}
	return checkDeepSeekCommandVersion(ctx, program, arguments)
}

func checkDeepSeekCommandVersion(ctx context.Context, executable string, arguments []string) error {
	version, err := deepseekacp.Version(ctx, executable, arguments)
	if err != nil {
		return err
	}
	if version != deepseek.Version {
		return fmt.Errorf("pitot: unsupported DeepSeek version; install @deepseek-ai/dsh@%s", deepseek.Version)
	}
	return nil
}

func doctorDeepSeek(stdout io.Writer) error {
	report := &readinessReport{out: stdout}
	if executable := report.binary("dsh"); executable != "" {
		if err := checkDeepSeekVersion(context.Background(), executable); err != nil {
			report.result("DeepSeek version", "UNVERIFIED", "bounded version check failed or the version is unsupported; install @deepseek-ai/dsh@"+deepseek.Version, true)
		} else {
			report.result("DeepSeek version", "PASS", deepseek.Version, false)
		}
	}
	root, err := config.FindRoot(".")
	if err != nil {
		root = "."
	}
	if err := checkDeepSeekIntegration(root); err != nil {
		report.result("native shell plugin", "INVALID", "owned integration files are missing or changed; run pitot init --host deepseek after reviewing existing files", true)
	} else {
		report.result("native shell plugin", "FOUND", "owned integration files match this release", false)
	}
	report.selectedRuntime()
	fmt.Fprintln(stdout, "  live readiness: checked before each prompt by pitot dev")
	return report.finish()
}
