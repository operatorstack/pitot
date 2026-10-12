package deepseekacp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func ownedEnvironment(root string) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "XDG_") || strings.HasPrefix(upper, "DSH_") || strings.HasPrefix(upper, "NODE_") {
			continue
		}
		switch upper {
		case "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "TMPDIR", "TMP", "TEMP", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH", "BASH_ENV", "ENV":
			continue
		}
		env = append(env, entry)
	}
	home := filepath.Join(root, "home")
	temp := filepath.Join(root, "tmp")
	return append(env, "HOME="+home, "USERPROFILE="+home, "DSH_HOME="+filepath.Join(home, ".dsh"), "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "XDG_DATA_HOME="+filepath.Join(home, ".local/share"), "XDG_CACHE_HOME="+filepath.Join(root, "cache"), "APPDATA="+filepath.Join(home, "AppData/Roaming"), "LOCALAPPDATA="+filepath.Join(home, "AppData/Local"), "TMPDIR="+temp, "TMP="+temp, "TEMP="+temp, "DSH_TELEMETRY_DISABLED=1")
}

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64<<10 {
		return 0, errors.New("DeepSeek metadata output exceeded 64 KiB")
	}
	return b.Buffer.Write(p)
}

// Version captures only the version banner, using the same owned lookup roots
// and subprocess ownership as a live session.
func Version(ctx context.Context, executable string, arguments []string) (string, error) {
	root, err := os.MkdirTemp("", "pitot-deepseek-version-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(root)
	for _, name := range []string{"home", "tmp", "cache"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			return "", err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, append(append([]string{}, arguments...), "--version")...)
	cmd.Env = ownedEnvironment(root)
	cmd.Dir = root
	cmd.WaitDelay = 2 * time.Second
	var out, stderr limitedOutput
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	attach, release, err := ownProcess(ctx, cmd)
	if err != nil {
		return "", err
	}
	defer release()
	if err := cmd.Start(); err != nil {
		return "", err
	}
	if err := attach(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return "", err
	}
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("dsh --version failed: %w", err)
	}
	if stderr.Len() != 0 {
		return "", errors.New("dsh --version emitted unexpected diagnostics")
	}
	return strings.TrimSpace(out.String()), nil
}
