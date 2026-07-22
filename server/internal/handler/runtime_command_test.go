package handler

import (
	"context"
	"testing"
)

func TestInMemoryRuntimeCommandStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryRuntimeCommandStore()

	cmd, err := s.Create(ctx, &RuntimeCommand{RuntimeID: "rt1", Kind: RuntimeCommandLogs, Lines: 100})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if cmd.Status != RuntimeCommandPending {
		t.Fatalf("status = %q, want pending", cmd.Status)
	}

	// One in-flight command per runtime.
	if _, err := s.Create(ctx, &RuntimeCommand{RuntimeID: "rt1", Kind: RuntimeCommandRestart}); err != errRuntimeCommandInProgress {
		t.Fatalf("expected in-progress error, got %v", err)
	}

	has, _ := s.HasPending(ctx, "rt1")
	if !has {
		t.Fatal("expected pending")
	}
	popped, _ := s.PopPending(ctx, "rt1")
	if popped == nil || popped.ID != cmd.ID || popped.Status != RuntimeCommandRunning {
		t.Fatalf("pop = %+v", popped)
	}
	// No longer pending after claim.
	if has, _ := s.HasPending(ctx, "rt1"); has {
		t.Fatal("should not be pending after pop")
	}

	if err := s.Complete(ctx, cmd.ID, "log output"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	got, _ := s.Get(ctx, cmd.ID)
	if got.Status != RuntimeCommandCompleted || got.Output != "log output" {
		t.Fatalf("after complete: %+v", got)
	}

	// A new command is allowed once the prior one is terminal.
	if _, err := s.Create(ctx, &RuntimeCommand{RuntimeID: "rt1", Kind: RuntimeCommandRestart}); err != nil {
		t.Fatalf("create after terminal: %v", err)
	}

	// Different runtime is independent + Fail path.
	c2, _ := s.Create(ctx, &RuntimeCommand{RuntimeID: "rt2", Kind: RuntimeCommandRestart})
	s.PopPending(ctx, "rt2")
	s.Fail(ctx, c2.ID, "boom")
	g2, _ := s.Get(ctx, c2.ID)
	if g2.Status != RuntimeCommandFailed || g2.Error != "boom" {
		t.Fatalf("after fail: %+v", g2)
	}
}

// TestPendingRuntimeCommandForHeartbeat verifies the heartbeat attach maps a
// popped command to the matching pending action (the server→daemon wiring).
func TestPendingRuntimeCommandForHeartbeat(t *testing.T) {
	ctx := context.Background()
	h := &Handler{RuntimeCommandStore: NewInMemoryRuntimeCommandStore()}

	// Nothing pending → both nil.
	if r, l := h.pendingRuntimeCommandForHeartbeat(ctx, "rt1"); r != nil || l != nil {
		t.Fatalf("expected no pending, got restart=%v logs=%v", r, l)
	}

	// A restart command → PendingRestart carrying Force.
	rc, _ := h.RuntimeCommandStore.Create(ctx, &RuntimeCommand{RuntimeID: "rt1", Kind: RuntimeCommandRestart, Force: true})
	restart, logs := h.pendingRuntimeCommandForHeartbeat(ctx, "rt1")
	if logs != nil || restart == nil || restart.ID != rc.ID || !restart.Force {
		t.Fatalf("restart map wrong: restart=%+v logs=%v", restart, logs)
	}
	// It was claimed (running), so a second probe finds nothing.
	if r, l := h.pendingRuntimeCommandForHeartbeat(ctx, "rt1"); r != nil || l != nil {
		t.Fatalf("command should be claimed already")
	}

	// A logs command → PendingLogFetch carrying Lines.
	h.RuntimeCommandStore.Complete(ctx, rc.ID, "")
	lc, _ := h.RuntimeCommandStore.Create(ctx, &RuntimeCommand{RuntimeID: "rt1", Kind: RuntimeCommandLogs, Lines: 321})
	restart, logs = h.pendingRuntimeCommandForHeartbeat(ctx, "rt1")
	if restart != nil || logs == nil || logs.ID != lc.ID || logs.Lines != 321 {
		t.Fatalf("logs map wrong: restart=%v logs=%+v", restart, logs)
	}
}
