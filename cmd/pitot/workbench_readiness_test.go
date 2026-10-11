package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/operatorstack/pitot/config"
	"github.com/operatorstack/pitot/runtime"
)

// Start a real runtime first so the fixture preserves the platform's secure
// descriptor permissions when substituting a controlled health endpoint.
func readinessFixture(t *testing.T, slow bool) (string, <-chan struct{}, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	manager, err := runtime.Start(ctx, config.Config{}, io.Discard)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "runtime.json")
	done := make(chan error, 1)
	go func() {
		done <- runtime.NewServer(manager, strings.Repeat("a", 64), path, io.Discard, io.Discard).Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		manager.Close()
		if err := <-done; err != nil {
			t.Errorf("fixture runtime: %v", err)
		}
	})
	var descriptor runtime.Descriptor
	for expires := time.Now().Add(5 * time.Second); time.Now().Before(expires); {
		raw, err := os.ReadFile(path)
		if err == nil && json.Unmarshal(raw, &descriptor) == nil && descriptor.InstanceID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if descriptor.InstanceID == "" {
		t.Fatal("secure descriptor was not published")
	}
	started, canceled := make(chan struct{}, 1), make(chan struct{}, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+descriptor.Token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		select {
		case started <- struct{}{}:
		default:
		}
		if slow {
			select {
			case <-r.Context().Done():
				select {
				case canceled <- struct{}{}:
				default:
				}
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "instance_id": descriptor.InstanceID})
	}))
	t.Cleanup(endpoint.Close)
	descriptor.Endpoint = endpoint.URL
	raw, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path, started, canceled
}

func TestRuntimeReadinessBudgetCoversHealthCall(t *testing.T) {
	path, started, canceled := readinessFixture(t, true)
	begin := time.Now()
	err := waitForRuntime(context.Background(), path, make(chan error), 120*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "within timeout") {
		t.Fatalf("late health admitted readiness: %v", err)
	}
	if elapsed := time.Since(begin); elapsed > 400*time.Millisecond {
		t.Fatalf("health call exceeded readiness budget: %v", elapsed)
	}
	select {
	case <-started:
	default:
		t.Fatal("fixture never received a health probe")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("timed-out health request remained active")
	}
}

func TestRuntimeReadinessPreservesCallerCancellation(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			path, started, canceled := readinessFixture(t, true)
			ctx, cancel := context.WithCancel(context.Background())
			expected := context.Canceled
			if mode == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 120*time.Millisecond)
				expected = context.DeadlineExceeded
			} else {
				go func() {
					select {
					case <-started:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			defer cancel()
			begin := time.Now()
			err := waitForRuntime(ctx, path, make(chan error), 2*time.Second)
			if !errors.Is(err, expected) {
				t.Fatalf("caller error=%v, want %v", err, expected)
			}
			if elapsed := time.Since(begin); elapsed > 400*time.Millisecond {
				t.Fatalf("caller cancellation was delayed: %v", elapsed)
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("canceled health request remained active")
			}
		})
	}
}

func TestRuntimeReadinessHealthyAndServerFailure(t *testing.T) {
	path, _, _ := readinessFixture(t, false)
	if err := waitForRuntime(context.Background(), path, make(chan error), time.Second); err != nil {
		t.Fatalf("healthy runtime=%v", err)
	}
	failure := errors.New("startup failed")
	serveErr := make(chan error, 1)
	serveErr <- failure
	if err := waitForRuntime(context.Background(), path, serveErr, time.Second); !errors.Is(err, failure) {
		t.Fatalf("server failure=%v", err)
	}
}
