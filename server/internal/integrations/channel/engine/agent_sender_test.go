package engine

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A bot has no Multica account and never will, so inviting it to bind is an
// invitation nothing can accept — and the invitation is a card posted into a
// real conversation, so it is not a harmless no-op. An unresolvable PERSON
// still gets the invitation; that is the whole distinction SenderIsBot draws.
func TestRouter_UnresolvedBotSender_DropsInsteadOfInviting(t *testing.T) {
	h := newHarness(t)
	h.ident.err = ErrSenderUnbound

	msg := p2pMessage(t)
	msg.Source.SenderIsBot = true
	msg.Source.SenderID = "ou_some_other_bot"

	if err := h.router.Handle(context.Background(), msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r, _ := h.audit.last(); r != DropReasonUnboundUser {
		t.Fatalf("expected unbound_user audit, got %q", r)
	}
	if h.dedup.marks() != 1 {
		t.Fatalf("bot drop must finalize Mark, got %d", h.dedup.marks())
	}
	// Give the replier the same window the person-shaped test waits in, so a
	// pass here means "no invitation was sent", not "we looked too early".
	if waitFor(300*time.Millisecond, func() bool {
		for _, r := range h.replier.calls() {
			if r.Outcome == OutcomeNeedsBinding {
				return true
			}
		}
		return false
	}) {
		t.Fatal("a bot must not be sent a binding invitation")
	}
}

// The person path must keep working unchanged — SenderIsBot false is every
// adapter that cannot tell, not just the ones that say "not a bot".
func TestRouter_UnresolvedPersonSender_StillInvited(t *testing.T) {
	h := newHarness(t)
	h.ident.err = ErrSenderUnbound

	if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !waitFor(time.Second, func() bool {
		for _, r := range h.replier.calls() {
			if r.Outcome == OutcomeNeedsBinding && r.Sender == "ou_user_a" {
				return true
			}
		}
		return false
	}) {
		t.Fatal("an unbound person must still be invited to bind")
	}
}

func TestResolvedIdentity_IsAgent(t *testing.T) {
	t.Parallel()
	user := uuidFromString(t, "11111111-1111-1111-1111-111111111111")
	agent := uuidFromString(t, "22222222-2222-2222-2222-222222222222")
	if (ResolvedIdentity{UserID: user}).IsAgent() {
		t.Error("a plain user identity is not an agent")
	}
	if !(ResolvedIdentity{UserID: user, AgentID: agent}).IsAgent() {
		t.Error("an identity carrying an agent id is an agent")
	}
}

// creator_type has been ('member','agent') since migration 001, so an issue an
// agent asked for can say so. Crediting the human who owns that agent's
// installation would put words in someone's mouth: they may not have been in
// the conversation at all.
func TestRouter_IssueCommandFromAgent_CreditsTheAgent(t *testing.T) {
	agentID := uuidFromString(t, "33333333-3333-3333-3333-333333333333")
	ownerID := uuidFromString(t, "44444444-4444-4444-4444-444444444444")

	cases := []struct {
		name     string
		identity ResolvedIdentity
		wantType string
		wantID   pgtype.UUID
	}{
		{
			name:     "person",
			identity: ResolvedIdentity{UserID: ownerID},
			wantType: "member",
			wantID:   ownerID,
		},
		{
			name:     "agent",
			identity: ResolvedIdentity{UserID: ownerID, AgentID: agentID},
			wantType: "agent",
			wantID:   agentID,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.ident.id = tc.identity
			h.binder.appendResult = AppendResult{DedupMarked: true, IssueCommand: &IssueCommand{Title: "Fix login"}}
			h.issues.result = service.IssueCreateResult{
				Issue: db.Issue{ID: uuidFromString(t, "77777777-7777-7777-7777-777777777777"), Number: 42, Title: "Fix login"},
			}
			if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !h.issues.called {
				t.Fatal("expected issue create")
			}
			if got := h.issues.params.CreatorType; got != tc.wantType {
				t.Errorf("creator_type = %q, want %q", got, tc.wantType)
			}
			if got := h.issues.params.CreatorID; got != tc.wantID {
				t.Errorf("creator_id = %v, want %v", got, tc.wantID)
			}
		})
	}
}
