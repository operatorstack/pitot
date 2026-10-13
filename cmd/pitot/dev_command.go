package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const devWaitDelay = 2 * time.Second

func devCommand(host, program string, args []string) (string, []string, error) {
	if runtime.GOOS != "windows" || (host != "codex" && host != "qwen") {
		return program, args, nil
	}
	resolved, err := exec.LookPath(program)
	if err != nil {
		return "", nil, err
	}
	if !strings.EqualFold(filepath.Ext(resolved), ".cmd") {
		return resolved, args, nil
	}
	node, entry, err := npmDevCommand(host, resolved)
	if err != nil {
		return "", nil, fmt.Errorf("unsupported %s launcher %q: %w; use -- node ABSOLUTE_ENTRYPOINT ARGS for an explicit Node invocation", host, resolved, err)
	}
	return node, append([]string{entry}, args...), nil
}

// npmDevCommand recognizes the complete plain-Node wrapper emitted by npm's
// cmd-shim. Unknown wrappers may have extra setup or flags, so never bypass them.
func npmDevCommand(host, shim string) (string, string, error) {
	packages := map[string]string{"codex": "@openai/codex", "qwen": "@qwen-code/qwen-code"}
	pkgName, ok := packages[host]
	if !ok || !strings.EqualFold(filepath.Base(shim), host+".cmd") {
		return "", "", fmt.Errorf("unrecognized command name")
	}
	shim, err := filepath.Abs(shim)
	if err != nil {
		return "", "", err
	}
	wrapper, err := readRegularFile(shim)
	if err != nil {
		return "", "", err
	}
	dir := filepath.Dir(shim)
	packageRoot := filepath.Join(dir, "node_modules", filepath.FromSlash(pkgName))
	if strings.EqualFold(filepath.Base(dir), ".bin") && strings.EqualFold(filepath.Base(filepath.Dir(dir)), "node_modules") {
		packageRoot = filepath.Join(filepath.Dir(dir), filepath.FromSlash(pkgName))
	}
	data, err := readRegularFile(filepath.Join(packageRoot, "package.json"))
	if err != nil {
		return "", "", err
	}
	var manifest struct {
		Name string            `json:"name"`
		Bin  map[string]string `json:"bin"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", "", fmt.Errorf("invalid package manifest: %w", err)
	}
	target := manifest.Bin[host]
	expectedTarget := map[string]string{"codex": "bin/codex.js", "qwen": "cli-entry.js"}[host]
	if manifest.Name != pkgName || target != expectedTarget || !filepath.IsLocal(target) {
		return "", "", fmt.Errorf("package name or bin target does not match %s", pkgName)
	}
	entry := filepath.Join(packageRoot, target)
	switch strings.ToLower(filepath.Ext(entry)) {
	case ".js", ".mjs", ".cjs":
	default:
		return "", "", fmt.Errorf("bin target must be a JavaScript file")
	}
	realRoot, err := filepath.EvalSymlinks(packageRoot)
	if err != nil {
		return "", "", err
	}
	realEntry, err := filepath.EvalSymlinks(entry)
	if err != nil {
		return "", "", err
	}
	inside, err := filepath.Rel(realRoot, realEntry)
	if err != nil || !filepath.IsLocal(inside) {
		return "", "", fmt.Errorf("bin target escapes package")
	}
	if err := requireRegularFile(entry); err != nil {
		return "", "", err
	}
	relative, err := filepath.Rel(dir, entry)
	if err != nil {
		return "", "", err
	}
	relative = strings.ReplaceAll(relative, "/", `\`)
	if strings.ReplaceAll(string(wrapper), "\r\n", "\n") != npmNodeShim(relative) {
		return "", "", fmt.Errorf("wrapper is not a supported standard npm Node shim")
	}
	node := filepath.Join(dir, "node.exe")
	if info, err := os.Stat(node); err == nil {
		if !info.Mode().IsRegular() {
			return "", "", fmt.Errorf("sibling node.exe is not a regular file")
		}
	} else if os.IsNotExist(err) {
		node, err = exec.LookPath("node.exe")
		if err != nil {
			return "", "", err
		}
	} else {
		return "", "", err
	}
	return node, entry, nil
}

func requireRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	return nil
}

func readRegularFile(path string) ([]byte, error) {
	if err := requireRegularFile(path); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func npmNodeShim(target string) string {
	return "@ECHO off\nGOTO start\n:find_dp0\nSET dp0=%~dp0\nEXIT /b\n:start\nSETLOCAL\nCALL :find_dp0\n\nIF EXIST \"%dp0%\\node.exe\" (\n  SET \"_prog=%dp0%\\node.exe\"\n) ELSE (\n  SET \"_prog=node\"\n  SET PATHEXT=%PATHEXT:;.JS;=;%\n)\n\nendLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & \"%_prog%\"  \"%dp0%\\" + target + "\" %*\n"
}
