package platform_test

import (
	"testing"
	"time"

	"github.com/santoshguru/crm/internal/platform"
)

func TestEmitFillsDefaults(t *testing.T) {
	var got platform.Event
	e := platform.NewEmitterWithSink("crm", func(ev platform.Event) { got = ev })

	e.Emit(platform.Event{Type: "resource.discovered", Subject: "aws:1:r:vm/i-1"})

	if got.Module != "crm" {
		t.Fatalf("Module = %q, want the emitter's module id", got.Module)
	}
	if got.At.IsZero() {
		t.Fatal("At not defaulted")
	}
	if got.ID == "" {
		t.Fatal("ID not generated")
	}
}

func TestEmitPreservesExplicitFields(t *testing.T) {
	var got platform.Event
	e := platform.NewEmitterWithSink("crm", func(ev platform.Event) { got = ev })

	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	e.Emit(platform.Event{
		ID: "fixed-id", Module: "other", Type: "x", At: at,
		Links: []platform.CrossLink{{Kind: "asset", Module: "assethub", Ref: "r", Label: "l"}},
	})

	if got.ID != "fixed-id" || got.Module != "other" || !got.At.Equal(at) {
		t.Fatalf("explicit fields overwritten: %+v", got)
	}
	if len(got.Links) != 1 || got.Links[0].Module != "assethub" {
		t.Fatalf("Links = %+v", got.Links)
	}
}

// TestEmitWithoutSinkDoesNotPanic covers the log-only path used in production when no
// bus is configured.
func TestEmitWithoutSinkDoesNotPanic(_ *testing.T) {
	platform.NewEmitter("crm", "").Emit(platform.Event{Type: "x"})
	platform.NewEmitter("crm", "nats://localhost:4222").Emit(platform.Event{Type: "x"})
}
