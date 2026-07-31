package runtime

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/operatorstack/pitot/config"
)

// require_controller converts the silent observation-only allow into a fault
// (Locus strict-mode candidate strict-config-v1 / strict-flag-v1).
func TestRequireControllerFaultsInsteadOfAllowing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager, err := Start(ctx, config.Config{RequireController: true}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	response, err := manager.DeliverEvent(ctx, actionEvent(t, "act_strict_1", "true"))
	if response != nil {
		t.Fatalf("strict mode must not resolve, got %+v", response)
	}
	if !errors.Is(err, ErrControllerRequired) {
		t.Fatalf("expected ErrControllerRequired, got %v", err)
	}
}

// Without strict mode, observation-only stays allowed but is announced once
// per kind on the operator's channel (never silently).
func TestObservationOnlyIsAnnouncedOncePerKind(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stderr := &bytes.Buffer{}
	manager, err := Start(ctx, config.Config{}, stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	for i, id := range []string{"act_obs_1", "act_obs_2"} {
		response, deliverErr := manager.DeliverEvent(ctx, actionEvent(t, id, "true"))
		if response != nil || deliverErr != nil {
			t.Fatalf("delivery %d: expected observation-only nil/nil, got %+v %v", i, response, deliverErr)
		}
	}
	notice := "observation-only"
	if got := strings.Count(stderr.String(), notice); got != 1 {
		t.Fatalf("expected exactly one %q notice, got %d in %q", notice, got, stderr.String())
	}
}
