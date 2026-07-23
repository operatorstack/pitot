package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/operatorstack/pitot/config"
)

func startTestServer(t *testing.T) (string, *Client, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	manager, err := Start(ctx, config.Config{}, io.Discard)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "runtime.json")
	done := make(chan error, 1)
	go func() { done <- NewServer(manager, strings.Repeat("a", 64), path, io.Discard, io.Discard).Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		manager.Close()
		if err := <-done; err != nil {
			t.Errorf("serve runtime: %v", err)
		}
	})
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		client, openErr := OpenClient(path)
		if openErr == nil {
			return path, client, cancel
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("runtime descriptor was not published")
	return "", nil, cancel
}

func TestDescriptorIsOwnerOnlyAndStrict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.json")
	descriptor := Descriptor{SchemaVersion: 1, InstanceID: "instance", PID: os.Getpid(), Endpoint: "http://127.0.0.1:1", Token: "secret", ConfigSHA256: strings.Repeat("a", 64)}
	if err := writeDescriptor(path, descriptor); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("descriptor mode = %v", info.Mode().Perm())
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenClient(path); err == nil {
			t.Fatal("expected group-readable descriptor to be rejected")
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil || value["token"] != "secret" {
		t.Fatalf("descriptor = %s, err = %v", raw, err)
	}
}

func TestTransportRejectsWrongTokenAndIdentity(t *testing.T) {
	_, client, _ := startTestServer(t)
	wrongToken := *client
	wrongToken.descriptor.Token = "wrong"
	if err := wrongToken.Health(context.Background()); err == nil {
		t.Fatal("wrong token unexpectedly authenticated")
	}
	wrongIdentity := *client
	wrongIdentity.descriptor.InstanceID = "stale-instance"
	if err := wrongIdentity.Health(context.Background()); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("identity error = %v", err)
	}
}

func TestTransportRejectsMalformedOversizedAndMissingControllerRequests(t *testing.T) {
	_, client, _ := startTestServer(t)
	request, err := http.NewRequest(http.MethodPost, client.descriptor.Endpoint+"/v1/requests", bytes.NewBufferString(`{"unknown":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+client.descriptor.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed status = %d", response.StatusCode)
	}

	oversized, err := http.NewRequest(http.MethodPost, client.descriptor.Endpoint+"/v1/events", strings.NewReader(strings.Repeat("x", maxRequestBytes+1)))
	if err != nil {
		t.Fatal(err)
	}
	oversized.Header.Set("Authorization", "Bearer "+client.descriptor.Token)
	response, err = http.DefaultClient.Do(oversized)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized status = %d", response.StatusCode)
	}
}

func TestLiveDescriptorCannotBeReplaced(t *testing.T) {
	path, _, _ := startTestServer(t)
	if err := prepareRuntimePath(path); err == nil || !strings.Contains(err.Error(), "live instance") {
		t.Fatalf("prepare live descriptor = %v", err)
	}
}
