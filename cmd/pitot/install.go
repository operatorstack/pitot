// Install wires a project to the typed Pitot SDKs from our own registry,
// through the distribution front door — never public npm/PyPI
// (packages-come-from-our-registry-or-nowhere). Bindings are pinned to the
// CLI's own release version (bindings-move-in-lockstep-with-the-CLI).
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/operatorstack/pitot/hydrate"
)

const (
	npmScope      = "@operatorstack"
	npmClientPkg  = "@operatorstack/pitot"
	pyClientPkg   = "operatorstack-pitot"
	pyRegistryRef = ".pitot/registry"
)

func runInstall(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("pitot install: requires a language (typescript, python)")
	}
	lang := args[0]
	configureOnly := false
	revert := false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--configure-only":
			configureOnly = true
		case "--revert":
			revert = true
		case "--host":
			if i+1 >= len(args) {
				return errors.New("pitot install: --host requires a value")
			}
			os.Setenv(hydrate.EnvHost, args[i+1])
			i++
		default:
			return fmt.Errorf("pitot install: unexpected argument %q", args[i])
		}
	}

	switch lang {
	case "typescript":
		if revert {
			return revertNPM(stdout)
		}
		return installNPM(stdout, configureOnly)
	case "python":
		if revert {
			return revertPython(stdout)
		}
		return installPython(stdout, configureOnly)
	default:
		return fmt.Errorf("pitot install: unsupported language %q (want typescript, python)", lang)
	}
}

// npmRegistryLine is the single scoped line install owns in .npmrc: only the
// @operatorstack scope resolves through the front door; everything else stays
// on public registries.
func npmRegistryLine() string {
	return npmScope + ":registry=" + hydrate.BaseURL() + "/npm/"
}

func installNPM(stdout io.Writer, configureOnly bool) error {
	if err := upsertNPMRC(npmRegistryLine()); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "configured .npmrc: %s\n", npmRegistryLine())
	spec := npmClientPkg + versionSpec("@")
	if configureOnly {
		fmt.Fprintf(stdout, "next: npm install %s\n", spec)
		return nil
	}
	return runTool(stdout, "npm", "install", spec)
}

func installPython(stdout io.Writer, configureOnly bool) error {
	index := hydrate.BaseURL() + "/pip/simple/"
	if err := os.MkdirAll(filepath.Dir(pyRegistryRef), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(pyRegistryRef, []byte("PIP_INDEX_URL="+index+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "configured %s: PIP_INDEX_URL=%s\n", pyRegistryRef, index)
	spec := pyClientPkg + versionSpec("==")
	if configureOnly {
		fmt.Fprintf(stdout, "next: pip install --index-url %s %s\n", index, spec)
		return nil
	}
	if _, err := exec.LookPath("uv"); err == nil {
		return runTool(stdout, "uv", "pip", "install", "--index-url", index, spec)
	}
	return runTool(stdout, "pip", "install", "--index-url", index, spec)
}

// versionSpec pins the binding to the CLI's own release version; a dev build
// cannot vouch for a binding version and installs unpinned with a warning.
func versionSpec(separator string) string {
	if v := releaseVersion(); v != "dev" {
		return separator + v
	}
	return ""
}

func upsertNPMRC(line string) error {
	existing, err := os.ReadFile(".npmrc")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var kept []string
	for _, l := range strings.Split(string(existing), "\n") {
		if l == "" || strings.HasPrefix(l, npmScope+":registry=") {
			continue
		}
		kept = append(kept, l)
	}
	kept = append(kept, line)
	return os.WriteFile(".npmrc", []byte(strings.Join(kept, "\n")+"\n"), 0o644)
}

func revertNPM(stdout io.Writer) error {
	existing, err := os.ReadFile(".npmrc")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var kept []string
	for _, l := range strings.Split(strings.TrimRight(string(existing), "\n"), "\n") {
		if strings.HasPrefix(l, npmScope+":registry=") {
			continue
		}
		kept = append(kept, l)
	}
	if len(kept) == 0 {
		fmt.Fprintln(stdout, "removed .npmrc")
		return os.Remove(".npmrc")
	}
	fmt.Fprintln(stdout, "removed the "+npmScope+" registry line from .npmrc")
	return os.WriteFile(".npmrc", []byte(strings.Join(kept, "\n")+"\n"), 0o644)
}

func revertPython(stdout io.Writer) error {
	if err := os.Remove(pyRegistryRef); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintln(stdout, "removed "+pyRegistryRef)
	return nil
}

func runTool(stdout io.Writer, name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Stdout = stdout
	command.Stderr = stdout
	if releaseVersion() == "dev" {
		fmt.Fprintln(stdout, "warning: dev build — installing the binding unpinned; a released pitot pins bindings to its own version")
	}
	if err := command.Run(); err != nil {
		return fmt.Errorf("pitot install: %s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}
