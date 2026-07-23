package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/operatorstack/pitot/bridge"
	"github.com/operatorstack/pitot/schema"
)

const (
	descriptorVersion = 1
	maxRequestBytes   = 4 << 20
)

// Descriptor is the owner-only capability used by local Pitot clients.
type Descriptor struct {
	SchemaVersion int    `json:"schema_version"`
	InstanceID    string `json:"instance_id"`
	PID           int    `json:"pid"`
	Endpoint      string `json:"endpoint"`
	Token         string `json:"token"`
	ConfigSHA256  string `json:"config_sha256"`
}

// Server exposes one authenticated loopback ingress for hooks and explicit requests.
type Server struct {
	manager     *Manager
	configSHA   string
	runtimePath string
	stdout      io.Writer
	stderr      io.Writer
}

// NewServer builds a local transport around manager.
func NewServer(manager *Manager, configSHA, runtimePath string, stdout, stderr io.Writer) *Server {
	return &Server{manager: manager, configSHA: configSHA, runtimePath: runtimePath, stdout: stdout, stderr: stderr}
}

// Serve publishes the runtime descriptor only after the authenticated endpoint is ready.
func (s *Server) Serve(ctx context.Context) error {
	if err := prepareRuntimePath(s.runtimePath); err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("pitot: bind loopback runtime: %w", err)
	}
	defer listener.Close()
	instanceID, err := randomID(16)
	if err != nil {
		return err
	}
	token, err := randomID(32)
	if err != nil {
		return err
	}
	descriptor := Descriptor{
		SchemaVersion: descriptorVersion,
		InstanceID:    instanceID,
		PID:           os.Getpid(),
		Endpoint:      "http://" + listener.Addr().String(),
		Token:         token,
		ConfigSHA256:  s.configSHA,
	}
	if err := writeDescriptor(s.runtimePath, descriptor); err != nil {
		return err
	}
	defer removeDescriptorIfOwned(s.runtimePath, instanceID)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.authorize(token, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"schema_version": descriptorVersion, "instance_id": instanceID})
	}))
	mux.HandleFunc("POST /v1/events", s.authorize(token, s.handleEvent))
	mux.HandleFunc("POST /v1/requests", s.authorize(token, s.handleRequest))
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()
	fmt.Fprintf(s.stdout, "Pitot %s — runtime ready\n", schema.Version)
	fmt.Fprintf(s.stdout, "runtime: %s\n", s.runtimePath)
	fmt.Fprintf(s.stdout, "config sha256: %s\n", s.configSHA)
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("pitot: shut down runtime: %w", err)
		}
		return nil
	}
}

func (s *Server) authorize(token string, next http.HandlerFunc) http.HandlerFunc {
	expected := sha256.Sum256([]byte("Bearer " + token))
	return func(w http.ResponseWriter, request *http.Request) {
		provided := sha256.Sum256([]byte(request.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, request)
	}
}

func (s *Server) handleEvent(w http.ResponseWriter, request *http.Request) {
	var event schema.Event
	if err := decodeRequest(w, request, &event); err != nil {
		return
	}
	if event.PitotVersion != schema.Version || event.Type != schema.TypeActionRequested || event.Action == nil || event.Action.ID == "" || event.Action.Kind == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid action event"})
		return
	}
	response, err := s.manager.DeliverEvent(request.Context(), event)
	if err != nil {
		fmt.Fprintf(s.stderr, "pitot: action %s resolved with boundary fault: %v\n", event.Action.ID, err)
	}
	if response == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleRequest(w http.ResponseWriter, request *http.Request) {
	var control schema.ControlRequested
	if err := decodeRequest(w, request, &control); err != nil {
		return
	}
	if control.PitotVersion != schema.Version || control.Type != schema.TypeControlRequested || control.Kind == "" || control.ActionID == "" || !validJSON(control.Data) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid control request"})
		return
	}
	response, err := s.manager.Request(request.Context(), control)
	if errors.Is(err, bridge.ErrNoController) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no controller registered"})
		return
	}
	if err != nil {
		fmt.Fprintf(s.stderr, "pitot: request %s resolved with boundary fault: %v\n", control.ActionID, err)
	}
	writeJSON(w, http.StatusOK, response)
}

func decodeRequest(w http.ResponseWriter, request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(w, request.Body, maxRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "trailing request data"})
		return errors.New("trailing request data")
	}
	return nil
}

func validJSON(value json.RawMessage) bool {
	return len(value) == 0 || json.Valid(value)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// Client uses a runtime capability without exposing its token to child roles.
type Client struct {
	descriptor Descriptor
	http       *http.Client
}

// OpenClient validates and loads an owner-only runtime descriptor.
func OpenClient(path string) (*Client, error) {
	descriptor, err := loadDescriptor(path)
	if err != nil {
		return nil, err
	}
	return &Client{descriptor: descriptor, http: &http.Client{Timeout: 35 * time.Second}}, nil
}

// Health proves the descriptor still identifies its live runtime.
func (c *Client) Health(ctx context.Context) error {
	var response struct {
		SchemaVersion int    `json:"schema_version"`
		InstanceID    string `json:"instance_id"`
	}
	status, err := c.call(ctx, http.MethodGet, "/v1/health", nil, &response)
	if err != nil {
		return err
	}
	if status != http.StatusOK || response.SchemaVersion != descriptorVersion || response.InstanceID != c.descriptor.InstanceID {
		return errors.New("pitot: runtime descriptor identity mismatch")
	}
	return nil
}

// DeliverEvent returns nil when no Controller is registered for the action kind.
func (c *Client) DeliverEvent(ctx context.Context, event schema.Event) (*schema.ControlResponse, error) {
	var response schema.ControlResponse
	status, err := c.call(ctx, http.MethodPost, "/v1/events", event, &response)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("pitot: runtime rejected event with HTTP %d", status)
	}
	return &response, nil
}

// Request sends an explicit correlated control request.
func (c *Client) Request(ctx context.Context, request schema.ControlRequested) (schema.ControlResponse, error) {
	var response schema.ControlResponse
	status, err := c.call(ctx, http.MethodPost, "/v1/requests", request, &response)
	if err != nil {
		return schema.ControlResponse{}, err
	}
	if status == http.StatusNotFound {
		return schema.ControlResponse{}, bridge.ErrNoController
	}
	if status != http.StatusOK {
		return schema.ControlResponse{}, fmt.Errorf("pitot: runtime rejected request with HTTP %d", status)
	}
	return response, nil
}

func (c *Client) call(ctx context.Context, method, path string, input, output any) (int, error) {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.descriptor.Endpoint+path, body)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+c.descriptor.Token)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, fmt.Errorf("pitot: contact runtime: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent {
		return response.StatusCode, nil
	}
	if response.StatusCode == http.StatusOK && output != nil {
		decoder := json.NewDecoder(io.LimitReader(response.Body, maxRequestBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(output); err != nil {
			return response.StatusCode, fmt.Errorf("pitot: decode runtime response: %w", err)
		}
	}
	return response.StatusCode, nil
}

// NewActionID returns an unpredictable correlation identifier.
func NewActionID() (string, error) {
	value, err := randomID(16)
	if err != nil {
		return "", err
	}
	return "act_" + value, nil
}

func randomID(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("pitot: generate runtime identity: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func writeDescriptor(path string, descriptor Descriptor) error {
	if path == "" {
		return errors.New("pitot: runtime descriptor path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("pitot: create runtime directory: %w", err)
	}
	encoded, err := json.MarshalIndent(descriptor, "", "  ")
	if err != nil {
		return err
	}
	suffix, err := randomID(8)
	if err != nil {
		return err
	}
	temporary := path + "." + suffix + ".tmp"
	defer os.Remove(temporary)
	if err := writeSecureDescriptorFile(temporary, append(encoded, '\n')); err != nil {
		return fmt.Errorf("pitot: write runtime descriptor: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("pitot: publish runtime descriptor: %w", err)
	}
	return validateDescriptorSecurity(path)
}

func loadDescriptor(path string) (Descriptor, error) {
	if err := validateDescriptorSecurity(path); err != nil {
		return Descriptor{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Descriptor{}, fmt.Errorf("pitot: read runtime descriptor: %w", err)
	}
	var descriptor Descriptor
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&descriptor); err != nil {
		return Descriptor{}, fmt.Errorf("pitot: decode runtime descriptor: %w", err)
	}
	if descriptor.SchemaVersion != descriptorVersion || descriptor.InstanceID == "" || descriptor.Endpoint == "" || descriptor.Token == "" || descriptor.ConfigSHA256 == "" {
		return Descriptor{}, errors.New("pitot: invalid runtime descriptor")
	}
	return descriptor, nil
}

func prepareRuntimePath(path string) error {
	if path == "" {
		return errors.New("pitot: runtime descriptor path is required")
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("pitot: inspect runtime descriptor: %w", err)
	}
	client, err := OpenClient(path)
	if err != nil {
		return fmt.Errorf("pitot: refusing to replace an invalid runtime descriptor: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if client.Health(ctx) == nil {
		return errors.New("pitot: runtime descriptor already belongs to a live instance")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("pitot: remove stale runtime descriptor: %w", err)
	}
	return nil
}

func removeDescriptorIfOwned(path, instanceID string) {
	descriptor, err := loadDescriptor(path)
	if err == nil && descriptor.InstanceID == instanceID {
		_ = os.Remove(path)
	}
}
