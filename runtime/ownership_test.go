package runtime

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/operatorstack/pitot/config"
)

type ownershipReadyWriter struct{ childPIDPath string }

func (w ownershipReadyWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "runtime ready") {
		child := exec.Command(os.Args[0], "-test.run=^TestOwnershipProcess$")
		child.Env = append(os.Environ(), "PITOT_OWNERSHIP_CHILD=1")
		if err := child.Start(); err != nil {
			return 0, err
		}
		if err := os.WriteFile(w.childPIDPath, []byte("pid="+strconv.Itoa(child.Process.Pid)), 0600); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// These subprocesses exercise OS locking and handle inheritance across exec.
func TestOwnershipProcess(t *testing.T) {
	if os.Getenv("PITOT_OWNERSHIP_CHILD") == "1" {
		for {
			if err := os.WriteFile(os.Getenv("PITOT_OWNERSHIP_CHILD_PID")+".heartbeat", []byte("heartbeat:"+strconv.FormatInt(time.Now().UnixNano(), 10)), 0600); err != nil {
				os.Exit(2)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	mode := os.Getenv("PITOT_OWNERSHIP_MODE")
	if mode == "" {
		return
	}
	if gate := os.Getenv("PITOT_OWNERSHIP_GATE"); gate != "" {
		if err := os.WriteFile(os.Getenv("PITOT_OWNERSHIP_START_READY"), []byte("ready"), 0600); err != nil {
			os.Exit(2)
		}
		for {
			if _, err := os.Stat(gate); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	manager, err := Start(context.Background(), config.Config{}, io.Discard)
	if err != nil {
		os.Exit(2)
	}
	defer manager.Close()
	var out io.Writer = io.Discard
	if mode == "with-child" {
		out = ownershipReadyWriter{os.Getenv("PITOT_OWNERSHIP_CHILD_PID")}
	}
	if err := NewServer(manager, strings.Repeat("a", 64), os.Getenv("PITOT_OWNERSHIP_PATH"), out, io.Discard).Serve(context.Background()); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func ownershipProcess(t *testing.T, path, mode string, extra ...string) (*exec.Cmd, <-chan error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestOwnershipProcess$")
	cmd.Env = append(os.Environ(), "PITOT_OWNERSHIP_MODE="+mode, "PITOT_OWNERSHIP_PATH="+path)
	cmd.Env = append(cmd.Env, extra...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd, done
}

func ownershipClient(t *testing.T, path string) *Client {
	t.Helper()
	for expires := time.Now().Add(10 * time.Second); time.Now().Before(expires); {
		c, err := OpenClient(path)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			err = c.Health(ctx)
			cancel()
			if err == nil {
				return c
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no healthy owner appeared")
	return nil
}

func TestOwnershipSeparateProcessStarters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.json")
	gate := filepath.Join(dir, "start")
	const count = 8
	cmds := make([]*exec.Cmd, count)
	done := make([]<-chan error, count)
	for i := 0; i < count; i++ {
		ready := filepath.Join(dir, fmt.Sprintf("ready-%d", i))
		cmds[i], done[i] = ownershipProcess(t, path, "server", "PITOT_OWNERSHIP_GATE="+gate, "PITOT_OWNERSHIP_START_READY="+ready)
		waitFor(t, ready, "ready")
	}
	if err := os.WriteFile(gate, []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	client := ownershipClient(t, path)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	winner := 0
	for i, cmd := range cmds {
		if cmd.Process.Pid == client.descriptor.PID {
			winner++
			continue
		}
		select {
		case err := <-done[i]:
			if err == nil {
				t.Fatal("competing server unexpectedly succeeded")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("another starter is still serving the same path")
		}
	}
	if winner != 1 {
		t.Fatalf("owner PID %d matched %d children", client.descriptor.PID, winner)
	}
	current, err := os.ReadFile(path)
	if err != nil || string(current) != string(original) {
		t.Fatalf("loser changed winner descriptor: %v", err)
	}
	if err := client.Health(context.Background()); err != nil {
		t.Fatalf("winner is unhealthy: %v", err)
	}
}

func TestOwnershipRefusesUncertainLegacyHealth(t *testing.T) {
	for _, mode := range []string{"slow", "identity", "malformed", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			closed, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			destination := "http://" + closed.Addr().String() + "/v1/health"
			closed.Close()
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "slow":
					time.Sleep(time.Second)
					io.WriteString(w, `{"schema_version":1,"instance_id":"owner"}`)
				case "identity":
					io.WriteString(w, `{"schema_version":1,"instance_id":"other"}`)
				case "malformed":
					io.WriteString(w, `invalid`)
				case "redirect":
					http.Redirect(w, r, destination, http.StatusFound)
				}
			}))
			defer endpoint.Close()
			path := filepath.Join(t.TempDir(), "runtime.json")
			d := Descriptor{SchemaVersion: 1, InstanceID: "owner", PID: os.Getpid(), Endpoint: endpoint.URL, Token: "secret", ConfigSHA256: strings.Repeat("a", 64)}
			if err := writeDescriptor(path, d); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			if err := prepareRuntimePath(path); err == nil {
				t.Fatal("uncertain health admitted takeover")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatalf("live descriptor changed: %v", err)
			}
		})
	}
}

func TestOwnershipGracefulHandoverKeepsLockFile(t *testing.T) {
	path, _, cancel := startTestServer(t)
	before, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	for expires := time.Now().Add(5 * time.Second); time.Now().Before(expires); {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var lock *os.File
	for expires := time.Now().Add(5 * time.Second); time.Now().Before(expires); {
		lock, err = acquireRuntimeOwnership(path)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lock == nil {
		t.Fatalf("ownership did not release after graceful shutdown: %v", err)
	}
	defer lock.Close()
	after, err := os.Stat(path + ".lock")
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("lock inode changed: %v", err)
	}
}

func TestOwnershipOwnerCrashDoesNotLeaveInheritedLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.json")
	pidPath := filepath.Join(dir, "child-pid")
	owner, done := ownershipProcess(t, path, "with-child", "PITOT_OWNERSHIP_CHILD_PID="+pidPath)
	ownershipClient(t, path)
	raw := waitFor(t, pidPath, "pid=")
	pid, err := strconv.Atoi(strings.TrimPrefix(raw, "pid="))
	if err != nil {
		t.Fatal(err)
	}
	child, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Kill(); _ = child.Release() })
	heartbeat := pidPath + ".heartbeat"
	waitFor(t, heartbeat, "heartbeat:")
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("owner did not stop")
	}
	old := waitFor(t, heartbeat, "heartbeat:")
	alive := false
	for expires := time.Now().Add(time.Second); time.Now().Before(expires); {
		b, _ := os.ReadFile(heartbeat)
		if len(b) > 0 && string(b) != old {
			alive = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !alive {
		t.Fatal("spawned child did not survive owner crash")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager, err := Start(ctx, config.Config{}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	served := make(chan error, 1)
	go func() { served <- NewServer(manager, strings.Repeat("a", 64), path, io.Discard, io.Discard).Serve(ctx) }()
	ownershipClient(t, path)
	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("successor did not stop")
	}
}

func TestOwnershipRejectsNonregularLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.json")
	if err := os.Mkdir(path+".lock", 0700); err != nil {
		t.Fatal(err)
	}
	if file, err := acquireRuntimeOwnership(path); err == nil {
		file.Close()
		t.Fatal("directory accepted as lock")
	}
}
