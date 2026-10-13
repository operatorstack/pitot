package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	pitotruntime "github.com/operatorstack/pitot/runtime"
)

// A component can pass its static check without proving that an agent session
// actually called a Controller. Keep those two results separate in the report.
type readinessReport struct {
	out    io.Writer
	issues int
}

func (r *readinessReport) result(component, status, detail string, issue bool) {
	fmt.Fprintf(r.out, "  %s: %s — %s\n", component, status, detail)
	if issue {
		r.issues++
	}
}

func (r *readinessReport) binary(name string) string {
	return r.lookup("binary "+name, name)
}

func (r *readinessReport) lookup(component, name string) string {
	path, err := doctorLookPath(name)
	if err != nil {
		r.result(component, "MISSING", "install the executable or add it to PATH", true)
		return ""
	}
	r.result(component, "FOUND", path, false)
	return path
}

func (r *readinessReport) file(component, path string, executable bool) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		r.result(component, "MISSING", "a readable regular file is required", true)
		return false
	}
	if executable && runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		r.result(component, "UNUSABLE", "the file is not executable", true)
		return false
	}
	r.result(component, "FOUND", filepath.Clean(path), false)
	return true
}

func (r *readinessReport) selectedRuntime() {
	path := os.Getenv("PITOT_RUNTIME")
	if path == "" {
		r.result("runtime", "NOT SELECTED", "hooks observe only until a runtime is selected; use pitot run or pitot dev", false)
		return
	}
	client, err := pitotruntime.OpenClient(path)
	if err != nil {
		// Descriptor parse errors can contain configuration values. Report the
		// category without printing descriptor bytes or capability tokens.
		r.result("runtime", "INVALID", "selected descriptor is missing, unreadable, insecure or malformed", true)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.LocalHealth(ctx); err != nil {
		r.result("runtime", "UNAVAILABLE", "local identity check failed; the descriptor may be stale or its endpoint invalid", true)
		return
	}
	r.result("runtime", "HEALTHY", "local runtime identity matched; Controller availability was not checked", false)
}

func (r *readinessReport) finish() error {
	r.result("live enforcement", "UNVERIFIED", "static inspection does not execute agent hooks or Controller actions", false)
	if r.issues != 0 {
		return fmt.Errorf("pitot doctor: static setup check found %d issue(s)", r.issues)
	}
	r.result("static setup", "PASS", "the inspected configuration and dependencies passed", false)
	return nil
}

// readDoctorFile caps configuration reads and avoids including parser errors or
// raw settings in user-facing diagnostics.
func readDoctorFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("configuration must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("configuration must be a regular file")
	}
	const limit = 1 << 20
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(data) > limit {
		return nil, errors.New("configuration could not be read within the size limit")
	}
	return data, nil
}
