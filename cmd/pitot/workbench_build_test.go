package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sdkRoot resolves the current repository from the package working directory.
func sdkRoot(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// initInto scaffolds a shell-policy project for lang inside a scratch
// repository root (so the tenant fragment lands in a temp .pitot/conf.d) and
// returns the absolute project directory. It fails the test on any init error.
func initInto(t *testing.T, lang string) string {
	t.Helper()
	root := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatal(err)
		}
	}()
	var out, errb bytes.Buffer
	if err := runInit([]string{"--language", lang, "--template", "shell-policy", "--dir", "proj"}, strings.NewReader(""), &out, &errb); err != nil {
		t.Fatalf("init %s: %v\n%s", lang, err, errb.String())
	}
	return filepath.Join(root, "proj")
}

// TestBuildGeneratedGoShellPolicy makes the Go build a first-class Step-5
// assertion: the generated controller compiles offline against the in-tree SDK
// via a filesystem module replace (no published dependency, no network).
func TestBuildGeneratedGoShellPolicy(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	// buildGeneratedShellPolicy performs runInit + local replace + offline build
	// and fails the test if the generated project does not compile.
	buildGeneratedShellPolicy(t)
}

// TestBuildGeneratedPythonImports verifies the generated Python controller
// imports cleanly against the local SDK (proving the SDK API surface it depends
// on exists), without contacting PyPI.
func TestBuildGeneratedPythonImports(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	sdk := filepath.Join(sdkRoot(t), "sdk", "python")
	if _, err := os.Stat(filepath.Join(sdk, "pitot", "runner.py")); err != nil {
		t.Fatalf("local python SDK not present at %s", sdk)
	}
	proj := initInto(t, "python")

	cmd := exec.Command(py, "-c", "import main; assert hasattr(main, 'handler'), 'generated main.py must define handler'")
	cmd.Dir = proj
	cmd.Env = append(os.Environ(),
		"PYTHONPATH="+sdk+string(os.PathListSeparator)+proj,
		"PYTHONDONTWRITEBYTECODE=1",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("python import of generated controller failed: %v\n%s", err, out)
	}
}

// TestTypeCheckGeneratedTypeScript type-checks the generated controller against
// the local SDK using a locally installed tsc. It skips (rather than fetching
// from a registry) when tsc or the local SDK is unavailable.
func TestTypeCheckGeneratedTypeScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available")
	}
	sdk := filepath.Join(sdkRoot(t), "sdk", "typescript")
	if _, err := os.Stat(filepath.Join(sdk, "package.json")); err != nil {
		t.Fatalf("local typescript SDK not present at %s", sdk)
	}
	proj := initInto(t, "typescript")

	// Resolve the genuine TypeScript compiler via node module resolution rather
	// than an ambient PATH `tsc` (which may be an unrelated decoy). Skip when the
	// typescript package is not locally installed — no registry fetch.
	resolve := exec.Command(node, "-e", "process.stdout.write(require.resolve('typescript/bin/tsc'))")
	resolve.Dir = proj
	tscJS, err := resolve.Output()
	if err != nil || len(tscJS) == 0 {
		t.Skip("typescript package not resolvable via node; skipping hermetic TS type-check")
	}

	// Make '@operatorstack/pitot' resolvable to the local SDK without installing.
	scope := filepath.Join(proj, "node_modules", "@operatorstack")
	if err := os.MkdirAll(scope, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sdk, filepath.Join(scope, "pitot")); err != nil {
		t.Fatalf("link local ts SDK: %v", err)
	}
	cmd := exec.Command(node, string(tscJS),
		"--noEmit", "--skipLibCheck", "--esModuleInterop",
		"--moduleResolution", "node", "--target", "es2022", "main.ts")
	cmd.Dir = proj
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tsc type-check of generated controller failed: %v\n%s", err, out)
	}
}

// TestCargoCheckGeneratedRust type-checks the generated Rust controller against
// the local SDK via a path dependency. It skips only when cargo is unavailable.
func TestCargoCheckGeneratedRust(t *testing.T) {
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo not available")
	}
	sdk := filepath.Join(sdkRoot(t), "sdk", "rust")
	if _, err := os.Stat(filepath.Join(sdk, "Cargo.toml")); err != nil {
		t.Fatalf("local rust SDK not present at %s", sdk)
	}
	proj := initInto(t, "rust")

	// Repoint the release-tag dependency at the local SDK by path.
	manifestPath := filepath.Join(proj, "Cargo.toml")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	patched := strings.Replace(string(manifest),
		fmt.Sprintf(`pitot = { git = "https://github.com/operatorstack/pitot", tag = "v%s" }`, pitotPackageVersion),
		`pitot = { path = `+quoteToml(sdk)+` }`, 1)
	if patched == string(manifest) {
		t.Fatalf("could not repoint rust SDK dependency in:\n%s", manifest)
	}
	if err := os.WriteFile(manifestPath, []byte(patched), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("cargo", "check", "--quiet")
	cmd.Dir = proj
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo check of generated controller failed: %v\n%s", err, out)
	}
}

// quoteToml renders a filesystem path as a TOML basic string.
func quoteToml(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
}
