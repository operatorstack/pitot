package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestLocalHealthRejectsNonlocalDescriptorsBeforeContact(t *testing.T) {
	for _, endpoint := range []string{
		"http://example.invalid:80", "http://localhost:80", "https://127.0.0.1:80",
		"http://user:secret@127.0.0.1:80", "http://127.0.0.1:80/path",
		"http://127.0.0.1:80?secret=value", "http://127.0.0.1:80?",
		"http://127.0.0.1:80#fragment", "http://[::1]:80", "http://127.0.0.1:0",
		"http://127.0.0.1:65536", "http://127.0.0.1:http",
	} {
		t.Run(endpoint, func(t *testing.T) {
			client := &Client{descriptor: Descriptor{Endpoint: endpoint, Token: "private"}}
			if err := client.LocalHealth(context.Background()); err == nil {
				t.Fatal("unsafe endpoint accepted")
			}
		})
	}
}

func TestLocalHealthRejectsRedirectAndBypassesProxy(t *testing.T) {
	var contacts atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contacts.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer trap.Close()
	t.Setenv("HTTP_PROXY", trap.URL)
	t.Setenv("HTTPS_PROXY", trap.URL)
	t.Setenv("ALL_PROXY", trap.URL)
	t.Setenv("NO_PROXY", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, trap.URL, http.StatusFound)
	}))
	defer server.Close()
	client := &Client{descriptor: Descriptor{Endpoint: server.URL, Token: "private"}}
	if err := client.LocalHealth(context.Background()); err == nil {
		t.Fatal("redirect accepted")
	}
	if contacts.Load() != 0 {
		t.Fatal("probe contacted a redirect or proxy destination")
	}
}

func TestLocalHealthRequiresIdentityAndHonorsDeadline(t *testing.T) {
	for _, scenario := range []string{"healthy", "wrong-identity", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/health" || r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer private" {
					t.Error("unexpected health request")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if scenario == "timeout" {
					<-r.Context().Done()
					return
				}
				identity := "expected"
				if scenario == "wrong-identity" {
					identity = "another-runtime"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "instance_id": identity})
			}))
			defer server.Close()
			client := &Client{descriptor: Descriptor{Endpoint: server.URL, Token: "private", InstanceID: "expected"}}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			err := client.LocalHealth(ctx)
			if (err == nil) != (scenario == "healthy") {
				t.Fatalf("unexpected health result: %v", err)
			}
		})
	}
}
