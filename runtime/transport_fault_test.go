package runtime

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/operatorstack/pitot/config"
)

// An errored delivery with no resolution must never read as an
// observation-only allow: the transport fails closed and the hook blocks.
func TestStrictDeliveryFaultIsNotAnAllow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager, err := Start(ctx, config.Config{RequireController: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "runtime.json")
	done := make(chan error, 1)
	go func() { done <- NewServer(manager, strings.Repeat("a", 64), path, io.Discard, io.Discard).Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		manager.Close()
		<-done
	})
	var client *Client
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if opened, openErr := OpenClient(path); openErr == nil {
			client = opened
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if client == nil {
		t.Fatal("runtime descriptor was not published")
	}
	response, err := client.DeliverEvent(ctx, actionEvent(t, "act_strict_http", "true"))
	if err == nil {
		t.Fatalf("strict fault must surface as an error, got response %+v", response)
	}
}
