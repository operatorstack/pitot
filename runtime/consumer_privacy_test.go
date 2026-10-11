package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/operatorstack/pitot/bridge"
	"github.com/operatorstack/pitot/config"
	"github.com/operatorstack/pitot/projection"
	"github.com/operatorstack/pitot/schema"
)

func TestProjectionCompositionPreservesSourceAndMetadata(t *testing.T) {
	raw := "controlled content"
	digest := sha256.Sum256([]byte(raw))
	full, _ := json.Marshal(raw)
	contents := []*schema.Content{nil, {Mode: schema.ContentFull, Full: full}, {Mode: schema.ContentSHA256, SHA256: hex.EncodeToString(digest[:])}, {Mode: schema.ContentOmit}}
	// Empty means rejection; absent is a successful missing measurement.
	expected := [][]string{{"absent", "absent", "absent"}, {"full", "sha256", "omit"}, {"", "sha256", "omit"}, {"", "", "omit"}}
	for i, supplied := range contents {
		for j, mode := range []projection.Mode{projection.Full, projection.SHA256, projection.Omit} {
			event := actionEvent(t, "preserved-action", raw)
			event.Content = supplied
			before, _ := json.Marshal(event)
			got, err := projectEvent(event, mode)
			want := expected[i][j]
			if want == "" {
				if err == nil {
					t.Fatalf("%d/%s: accepted unavailable representation", i, mode)
				}
				continue
			}
			if err != nil {
				t.Fatalf("%d/%s: %v", i, mode, err)
			}
			if want == "absent" {
				if got.Content != nil {
					t.Fatal("invented content")
				}
			} else {
				if got.Content == nil || got.Content.Mode != want {
					t.Fatalf("content=%+v want=%s", got.Content, want)
				}
				switch want {
				case "full":
					if !bytes.Equal(got.Content.Full, full) {
						t.Fatal("changed full content")
					}
				case "sha256":
					if got.Content.SHA256 != hex.EncodeToString(digest[:]) {
						t.Fatal("changed digest")
					}
				case "omit":
					if len(got.Content.Full) != 0 || got.Content.SHA256 != "" {
						t.Fatal("omit retained representation")
					}
				}
			}
			after, _ := json.Marshal(event)
			if !bytes.Equal(before, after) {
				t.Fatal("source mutated")
			}
			got.Content = event.Content
			if !reflect.DeepEqual(got, event) {
				t.Fatal("metadata changed")
			}
		}
	}
	contradictory := actionEvent(t, "invalid", raw)
	contradictory.Content = &schema.Content{Mode: schema.ContentSHA256, SHA256: hex.EncodeToString(digest[:]), Full: full}
	if _, err := projectEvent(contradictory, projection.Omit); err == nil {
		t.Fatal("omit concealed invalid source")
	}
}

type consumerDiagnosticBuffer struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	written chan struct{}
	once    sync.Once
}

func (b *consumerDiagnosticBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	if b.written != nil {
		b.once.Do(func() { close(b.written) })
	}
	return n, err
}
func (b *consumerDiagnosticBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type failedConsumerPipe struct{}

func (failedConsumerPipe) Write([]byte) (int, error) { return 0, errors.New("CONTROLLED_SECRET_ERROR") }
func (failedConsumerPipe) Close() error              { return nil }
func testConsumerWorker(stderr io.Writer) *consumerWorker {
	return &consumerWorker{id: "audit", events: map[string]struct{}{schema.TypeActionRequested: {}}, projection: projection.Omit, queue: make(chan schema.Event, 1), dead: make(chan struct{}), stderr: stderr}
}
func awaitConsumerSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("Consumer transition timed out")
	}
}

func TestConsumerTerminalFailureWithoutNextOffer(t *testing.T) {
	log := &consumerDiagnosticBuffer{written: make(chan struct{})}
	w := testConsumerWorker(log)
	w.stdin = failedConsumerPipe{}
	if err := w.offer(actionEvent(t, "CONTROLLED_SECRET_ID", "CONTROLLED_SECRET_CONTENT")); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { w.deliver(context.Background()); close(done) }()
	awaitConsumerSignal(t, done)
	w.stop("process-exited")
	w.close()
	text := log.String()
	if strings.Count(text, "delivery fault") != 1 || !strings.Contains(text, "write-failed") || !strings.Contains(text, "may be incomplete") || strings.Contains(text, "CONTROLLED_SECRET") {
		t.Fatalf("diagnostic=%q", text)
	}
	for i := 0; i < 1000; i++ {
		if err := w.offer(actionEvent(t, "later", "private")); err == nil {
			t.Fatal("dead worker admitted an event")
		}
	}
	filtered := actionEvent(t, "filtered", "private")
	filtered.Type = "other.event"
	if err := w.offer(filtered); err != nil {
		t.Fatal("filtered event must be a no-op")
	}
	if len(w.queue) != 0 {
		t.Fatal("dead worker enqueued an event")
	}
}

type blockedConsumerDiagnostic struct{ entered, release chan struct{} }

func (b blockedConsumerDiagnostic) Write(p []byte) (int, error) {
	close(b.entered)
	<-b.release
	return len(p), nil
}
func TestConsumerBlockedDiagnosticDoesNotHoldAdmission(t *testing.T) {
	log := blockedConsumerDiagnostic{make(chan struct{}), make(chan struct{})}
	w := testConsumerWorker(log)
	done := make(chan struct{})
	go func() { w.stop("write-failed"); close(done) }()
	awaitConsumerSignal(t, log.entered)
	offered := make(chan struct{})
	go func() {
		if err := w.offer(actionEvent(t, "later", "private")); err == nil {
			t.Error("dead worker accepted")
		}
		close(offered)
	}()
	awaitConsumerSignal(t, offered)
	close(log.release)
	awaitConsumerSignal(t, done)
}
func TestConsumerConcurrentAdmissionAndTerminalPublication(t *testing.T) {
	w := testConsumerWorker(io.Discard)
	event := actionEvent(t, "race", "private")
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for j := 0; j < 100; j++ {
				_ = w.offer(event)
			}
		}()
	}
	w.stop("")
	group.Wait()
	for i := 0; i < 100; i++ {
		if err := w.offer(event); err == nil {
			t.Fatal("admitted after terminal publication")
		}
	}
}

// Run the test binary itself for portable real process lifecycle witnesses.
func TestConsumerLifecycleHelper(t *testing.T) {
	switch os.Args[len(os.Args)-1] {
	case "consumer-exit":
		os.Exit(0)
	case "consumer-wait":
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
}
func TestConsumerProcessExitAndExpectedShutdown(t *testing.T) {
	for _, mode := range []string{"consumer-exit", "consumer-wait"} {
		t.Run(mode, func(t *testing.T) {
			log := &consumerDiagnosticBuffer{written: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := newConsumerWorker(ctx, config.ConsumerConfig{ID: "audit", Command: []string{os.Args[0], "-test.run=^TestConsumerLifecycleHelper$", "--", mode}, Events: []string{schema.TypeActionRequested}, Projection: config.ProjectionConfig{Content: projection.Omit}}, log)
			if w.startErr != nil {
				t.Fatal(w.startErr)
			}
			if mode == "consumer-exit" {
				awaitConsumerSignal(t, log.written)
				if !strings.Contains(log.String(), "process-exited") {
					t.Fatal(log.String())
				}
				if err := w.offer(actionEvent(t, "late", "private")); err == nil {
					t.Fatal("exited child admitted")
				}
			} else {
				cancel()
				w.close()
				awaitConsumerSignal(t, w.dead)
				// stop() after planned shutdown cannot publish another terminal diagnostic.
				w.stop("process-exited")
				if log.String() != "" {
					t.Fatal(log.String())
				}
			}
		})
	}
}

func TestRuntimeBoundaryDiagnosticsDoNotEchoEnvelopeIdentifiers(t *testing.T) {
	marker := "CONTROLLED_SECRET_IDENTIFIER"
	for _, eventIngress := range []bool{true, false} {
		t.Run(map[bool]string{true: "event", false: "request"}[eventIngress], func(t *testing.T) {
			log := &bytes.Buffer{}
			ctx := context.Background()
			manager, err := Start(ctx, config.Config{RequireController: true}, log)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			server := NewServer(manager, "", "", io.Discard, log)
			rec := httptest.NewRecorder()
			var body []byte
			if eventIngress {
				event := actionEvent(t, marker, marker)
				event.Action.Kind = marker
				body, _ = json.Marshal(event)
				server.handleEvent(rec, httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(body)))
				if rec.Code != http.StatusConflict {
					t.Fatal(rec.Code)
				}
			} else {
				reg := bridge.Registration{Kind: "shell", ControllerID: "configured", DeadlineMS: 100, OnTimeout: schema.OutcomeDeny, OnUnavailable: schema.OutcomeDeny}
				router := bridge.NewRouter()
				if err := router.Register(reg); err != nil {
					t.Fatal(err)
				}
				manager.controllers["shell"] = &controllerWorker{ctx: ctx, router: router, registration: reg, slot: make(chan struct{}, 1), startErr: errors.New(marker)}
				body, _ = json.Marshal(schema.ControlRequested{PitotVersion: schema.Version, Type: schema.TypeControlRequested, Kind: "shell", ActionID: marker})
				server.handleRequest(rec, httptest.NewRequest(http.MethodPost, "/v1/requests", bytes.NewReader(body)))
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"outcome":"deny"`) {
					t.Fatal(rec.Body.String())
				}
			}
			if strings.Contains(log.String(), marker) || !strings.Contains(log.String(), "boundary fault") {
				t.Fatalf("diagnostic=%q", log.String())
			}
		})
	}
	log := &bytes.Buffer{}
	manager, _ := Start(context.Background(), config.Config{}, log)
	defer manager.Close()
	event := actionEvent(t, marker, marker)
	event.Action.Kind = marker
	_, _ = manager.DeliverEvent(context.Background(), event)
	if strings.Contains(log.String(), marker) {
		t.Fatal(log.String())
	}
}

func TestConsumerCancellationPublishesTerminalState(t *testing.T) {
	log := &consumerDiagnosticBuffer{}
	w := testConsumerWorker(log)
	w.stdin = failedConsumerPipe{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.deliver(ctx)
	if err := w.offer(actionEvent(t, "late", "private")); err == nil {
		t.Fatal("cancelled delivery loop admitted")
	}
	if len(w.queue) != 0 || log.String() != "" {
		t.Fatalf("queue=%d diagnostic=%q", len(w.queue), log.String())
	}
}

type heldConsumerPipe struct {
	entered, release     chan struct{}
	enterOnce, closeOnce sync.Once
}

func (p *heldConsumerPipe) Write(raw []byte) (int, error) {
	p.enterOnce.Do(func() { close(p.entered) })
	<-p.release
	return len(raw), nil
}
func (p *heldConsumerPipe) Close() error { p.closeOnce.Do(func() { close(p.release) }); return nil }
func TestConsumerBackpressureRetainsControllerResolution(t *testing.T) {
	log := &consumerDiagnosticBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager, err := Start(ctx, config.Config{}, log)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	w := testConsumerWorker(log)
	pipe := &heldConsumerPipe{entered: make(chan struct{}), release: make(chan struct{})}
	w.stdin = pipe
	manager.consumers = []*consumerWorker{w}
	reg := bridge.Registration{Kind: "shell", ControllerID: "configured", DeadlineMS: 100, OnTimeout: schema.OutcomeDeny, OnUnavailable: schema.OutcomeDeny}
	router := bridge.NewRouter()
	if err := router.Register(reg); err != nil {
		t.Fatal(err)
	}
	manager.controllers["shell"] = &controllerWorker{ctx: ctx, router: router, registration: reg, slot: make(chan struct{}, 1)}
	if err := w.offer(actionEvent(t, "first", "private")); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { w.deliver(ctx); close(done) }()
	awaitConsumerSignal(t, pipe.entered)
	resolved := make(chan struct{})
	go func() {
		defer close(resolved)
		for _, id := range []string{"queued", "saturated"} {
			response, err := manager.DeliverEvent(ctx, actionEvent(t, id, "private"))
			if err != nil || response == nil || response.Outcome != schema.OutcomeDeny {
				t.Errorf("response=%+v err=%v", response, err)
			}
		}
	}()
	awaitConsumerSignal(t, resolved)
	if !strings.Contains(log.String(), "consumer queue is full") {
		t.Fatal(log.String())
	}
	w.close()
	awaitConsumerSignal(t, done)
}
