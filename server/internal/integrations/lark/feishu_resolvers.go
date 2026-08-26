package lark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
)

// This file is the Feishu ResolverSet: the platform-specific implementations
// the channel-agnostic engine.Router runs the inbound pipeline through. Each
// resolver translates between the engine's normalized channel.InboundMessage /
// engine types and the Feishu store / services. Platform-specific fields the
// normalized envelope does not carry (app_id, event_type, create time) are read
// from the original InboundMessage that feishuChannel stashes in
// channel.InboundMessage.Raw — the documented adapter boundary (the core never
// reads Raw).

// originFeishuChat is the issue.origin_type label written for issues created
// via the Feishu /issue command. Kept as "lark_chat" (unchanged from the
// pre-cutover dispatcher) so analytics classification does not shift.
const originFeishuChat = "lark_chat"

// larkMsgFromRaw decodes the original Feishu InboundMessage the feishuChannel
// stashed in channel.InboundMessage.Raw.
func larkMsgFromRaw(msg channel.InboundMessage) (InboundMessage, error) {
	var lm InboundMessage
	if len(msg.Raw) == 0 {
		return InboundMessage{}, errors.New("lark: inbound message Raw is empty")
	}
	if err := json.Unmarshal(msg.Raw, &lm); err != nil {
		return InboundMessage{}, fmt.Errorf("decode feishu inbound raw: %w", err)
	}
	return lm, nil
}

// NewFeishuResolverSet assembles the Feishu ResolverSet from the store, the
// shared session service, audit logger, and (optional) outbound replier +
// typing indicator. Feishu is just another consumer of the channel-agnostic
// engine.ChatSession — there is no Feishu-specific session implementation.
func NewFeishuResolverSet(store *ChannelStore, session *engine.ChatSession, audit AuditLogger, replier OutcomeReplier, typing *TypingIndicatorManager, media engine.MediaResolver, api APIClient, creds CredentialsResolver) engine.ResolverSet {
	set := engine.ResolverSet{
		Installation: &feishuInstallationResolver{store: store},
		Identity:     &feishuIdentityResolver{store: store, api: api, creds: creds, logger: slog.Default()},
		Dedup:        &feishuDeduper{store: store},
		Session:      &feishuSessionBinder{session: session},
		Audit:        &feishuAuditor{audit: audit},
		OriginType:   originFeishuChat,
	}
	if replier != nil {
		set.Replier = &feishuOutboundReplier{replier: replier}
	}
	if typing != nil {
		set.Typing = &feishuTypingNotifier{mgr: typing}
	}
	if media != nil {
		set.Media = media
	}
	return set
}

// ---- installation routing ----

type feishuInstallationResolver struct{ store *ChannelStore }

func (r *feishuInstallationResolver) ResolveInstallation(ctx context.Context, msg channel.InboundMessage) (engine.ResolvedInstallation, error) {
	lm, err := larkMsgFromRaw(msg)
	if err != nil {
		return engine.ResolvedInstallation{}, err
	}
	inst, err := r.store.GetLarkInstallationByAppID(ctx, lm.AppID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return engine.ResolvedInstallation{}, engine.ErrInstallationNotFound
		}
		return engine.ResolvedInstallation{}, err
	}
	return engine.ResolvedInstallation{
		ID:              inst.ID,
		WorkspaceID:     inst.WorkspaceID,
		AgentID:         inst.AgentID,
		InstallerUserID: inst.InstallerUserID,
		Active:          InstallationStatus(inst.Status) == InstallationActive,
		Platform:        inst,
	}, nil
}

// ---- identity ----

type feishuIdentityResolver struct {
	store *ChannelStore
	// api + creds are used only to recognize a bot sender; both may be nil on
	// a deployment without a wired Lark app, and the resolver degrades to the
	// person-only behaviour it had before.
	api    APIClient
	creds  CredentialsResolver
	logger *slog.Logger
}

func (r *feishuIdentityResolver) ResolveSender(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage) (engine.ResolvedIdentity, error) {
	binding, err := r.store.GetLarkUserBindingByOpenID(ctx, GetUserBindingByOpenIDParams{
		InstallationID: inst.ID,
		ChannelUserID:  msg.Source.SenderID,
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return engine.ResolvedIdentity{}, err
		}
		// No user binding. Before calling this an unbound stranger, check
		// whether the sender is another Multica agent's bot: an agent's
		// open_id lives on its own installation, so one agent @-mentioning
		// another resolves to a first-class actor instead of a dead end. A bot
		// has no account to bind, so without this the only outcome available
		// was to invite it to bind — which it cannot do, ever.
		return r.resolveAgentSender(ctx, inst, msg)
	}
	isMember, err := r.store.IsWorkspaceMember(ctx, inst.WorkspaceID, binding.MulticaUserID)
	if err != nil {
		return engine.ResolvedIdentity{}, err
	}
	if !isMember {
		return engine.ResolvedIdentity{}, engine.ErrSenderNotMember
	}
	return engine.ResolvedIdentity{UserID: binding.MulticaUserID}, nil
}

// senderBotName returns the sender's bot display name when the sender is a bot
// in this chat, reading it from the chat's own bot roster.
//
// The roster is used instead of comparing open_ids because a Lark open_id is
// scoped to the app that observes it: the same bot is a different open_id to
// every app that can see it, so the bot_open_id an installation stored for
// itself never equals what a SIBLING installation receives as the sender.
// Lark's bot roster returns only bot_id + bot_name — there is no app_id and no
// cross-app id anywhere in this API surface — so the name is the join key, and
// installationForBotName grounds the other side of that join in the same
// roster so both names come from one source at one moment.
//
// A failure is reported as "not a bot", which lands the sender back on the
// ordinary unbound-person path. It is logged, because a silent fallback here
// is indistinguishable from a sender that really is a person.
func (r *feishuIdentityResolver) senderBotName(ctx context.Context, self Installation, chatID string, senderID string) (string, bool) {
	bots, err := r.chatBots(ctx, self, chatID)
	if err != nil {
		r.log().Warn("lark identity: chat bot roster unavailable; treating sender as a person",
			"installation_id", uuidString(self.ID), "chat_id", chatID, "err", err)
		return "", false
	}
	for _, b := range bots {
		if b.OpenID == senderID {
			return b.Name, true
		}
	}
	return "", false
}

// installationForBotName finds which of this workspace's installations owns the
// named bot. Each candidate reads the SAME chat roster through its own
// credentials and locates itself by the bot_open_id it stored — an id that is
// correct in exactly that app's namespace — so the name it compares is its own,
// verified, and fetched at the same moment as the sender's.
func (r *feishuIdentityResolver) installationForBotName(ctx context.Context, self Installation, chatID, botName string) (Installation, bool) {
	if botName == "" {
		return Installation{}, false
	}
	peers, err := r.store.ListLarkInstallationsByWorkspace(ctx, self.WorkspaceID)
	if err != nil {
		r.log().Warn("lark identity: could not list workspace installations",
			"workspace_id", uuidString(self.WorkspaceID), "err", err)
		return Installation{}, false
	}
	var found Installation
	matches := 0
	for _, peer := range peers {
		if peer.ID == self.ID || peer.Status != "active" {
			continue
		}
		bots, err := r.chatBots(ctx, peer, chatID)
		if err != nil {
			continue
		}
		for _, b := range bots {
			if b.OpenID == peer.BotOpenID && b.Name == botName {
				found, matches = peer, matches+1
				break
			}
		}
	}
	if matches != 1 {
		// Zero: the bot belongs to no agent of ours. More than one: two of our
		// agents share a display name, and guessing which one acted would put
		// the wrong agent on an issue. Refuse rather than pick.
		if matches > 1 {
			r.log().Warn("lark identity: bot name is ambiguous across installations",
				"bot_name", botName, "matches", matches)
		}
		return Installation{}, false
	}
	return found, true
}

func (r *feishuIdentityResolver) chatBots(ctx context.Context, inst Installation, chatID string) ([]ChatBotMember, error) {
	creds, err := installationCredentialsFor(inst, r.creds)
	if err != nil {
		return nil, err
	}
	return r.api.ListChatBots(ctx, creds, ChatID(chatID))
}

func (r *feishuIdentityResolver) log() *slog.Logger {
	if r.logger != nil {
		return r.logger
	}
	return slog.Default()
}

// resolveAgentSender maps a bot sender to the Multica agent behind it. It runs
// only after the user-binding lookup missed.
//
// The installer stands in as UserID for everything that needs an accountable
// person, and is membership-checked like any other sender — so an agent whose
// installer has left the workspace stops being able to initiate, which is the
// same rule that applies to the human.
func (r *feishuIdentityResolver) resolveAgentSender(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage) (engine.ResolvedIdentity, error) {
	if r.api == nil || !r.api.IsConfigured() || msg.Source.ChatID == "" {
		return engine.ResolvedIdentity{}, engine.ErrSenderUnbound
	}
	self, err := r.store.GetLarkInstallation(ctx, inst.ID)
	if err != nil {
		return engine.ResolvedIdentity{}, err
	}
	botName, ok := r.senderBotName(ctx, self, msg.Source.ChatID, msg.Source.SenderID)
	if !ok {
		// Not a bot in this chat: an ordinary person who has not bound yet.
		return engine.ResolvedIdentity{}, engine.ErrSenderUnbound
	}
	src, ok := r.installationForBotName(ctx, self, msg.Source.ChatID, botName)
	if !ok {
		// A bot, but not one of this workspace's agents.
		return engine.ResolvedIdentity{}, engine.ErrSenderIsBot
	}
	if src.AgentID == inst.AgentID {
		// The agent's own bot. Lark does not echo an app's messages back to
		// itself, so this is only reachable if an installation is ever routed
		// twice; refusing keeps a self-trigger impossible by construction
		// rather than by platform behaviour.
		return engine.ResolvedIdentity{}, engine.ErrSenderUnbound
	}
	isMember, err := r.store.IsWorkspaceMember(ctx, inst.WorkspaceID, src.InstallerUserID)
	if err != nil {
		return engine.ResolvedIdentity{}, err
	}
	if !isMember {
		return engine.ResolvedIdentity{}, engine.ErrSenderNotMember
	}
	return engine.ResolvedIdentity{UserID: src.InstallerUserID, AgentID: src.AgentID}, nil
}

// ---- dedup ----

type feishuDeduper struct{ store *ChannelStore }

func (r *feishuDeduper) Claim(ctx context.Context, installationID pgtype.UUID, messageID string) (pgtype.UUID, error) {
	claim, err := r.store.ClaimLarkInboundDedup(ctx, ClaimInboundDedupParams{
		InstallationID: installationID,
		MessageID:      messageID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, engine.ErrDuplicate
		}
		return pgtype.UUID{}, err
	}
	return claim.ClaimToken, nil
}

func (r *feishuDeduper) Mark(ctx context.Context, installationID pgtype.UUID, messageID string, claimToken pgtype.UUID) error {
	_, err := r.store.MarkLarkInboundDedupProcessed(ctx, MarkInboundDedupProcessedParams{
		InstallationID: installationID,
		MessageID:      messageID,
		ClaimToken:     claimToken,
	})
	return err
}

func (r *feishuDeduper) Release(ctx context.Context, installationID pgtype.UUID, messageID string, claimToken pgtype.UUID) error {
	_, err := r.store.ReleaseLarkInboundDedup(ctx, ReleaseInboundDedupParams{
		InstallationID: installationID,
		MessageID:      messageID,
		ClaimToken:     claimToken,
	})
	return err
}

// ---- session bind / append ----

// chatSession is the slice of engine.ChatSession the Feishu binder drives.
// Declared as an interface so the (platform-specific) param mapping can be
// unit-tested with a fake; *engine.ChatSession is the production value.
type chatSession interface {
	EnsureSession(ctx context.Context, in engine.EnsureSessionInput) (pgtype.UUID, error)
	StartSession(ctx context.Context, in engine.StartSessionInput) (engine.StartSessionResult, error)
	MarkPendingFresh(ctx context.Context, sessionID pgtype.UUID, messageID string) error
	AppendUserMessage(ctx context.Context, in engine.AppendInput) (engine.AppendResult, error)
	BindMediaRefs(ctx context.Context, in engine.BindMediaInput) error
}

func (r *feishuSessionBinder) StartSession(ctx context.Context, p engine.StartSessionParams) (engine.StartSessionResult, error) {
	bindingKey, config := larkSessionRouting(p.Message)
	result, err := r.session.StartSession(ctx, engine.StartSessionInput{
		EnsureSessionInput: engine.EnsureSessionInput{
			WorkspaceID: p.Installation.WorkspaceID, AgentID: p.Installation.AgentID,
			InstallationID: p.Installation.ID, Sender: p.Creator,
			BindingKey: bindingKey, BindingConfig: config, ChatType: p.Message.Source.ChatType,
		},
		Initiator: p.Sender,
		Body:      p.Message.Text, MessageID: p.Message.MessageID, ThreadID: p.Message.Source.ThreadID,
		ClaimToken: p.ClaimToken, MediaPendingSeconds: p.MediaPendingSeconds,
		PersistMessage: p.PersistMessage, HistoryBoundaryPending: p.HistoryBoundaryPending,
		BeforeCommit: p.BeforeCommit,
	})
	return engine.StartSessionResult{SessionID: result.SessionID, BindingID: result.BindingID, RouteRevision: result.RouteRevision, Append: result.Append}, err
}

type feishuSessionBinder struct{ session chatSession }

// larkBindingConfig is the opaque outbound routing persisted on the chat
// binding's config when the binding key is a composite (Lark topic): the real
// chat id lives here so the outbound path can post back.
type larkBindingConfig struct {
	ChatID string `json:"chat_id"`
}

// larkSessionRouting derives the session-isolation key (stored as
// channel_chat_id) and the outbound config from one inbound Feishu message.
//
// A p2p chat is one continuous session per chat, so the key is the chat id and
// routes outbound alone (no config).
//
// A group conversation is isolated by its thread ROOT message — key =
// "chat:root" — NOT by the topic id. This is what lets the 话题 the bot opens by
// replying reply_in_thread continue the message that started it: in Lark a
// bot-created topic is rooted at the triggering message M, so the first
// top-level @ (which carries no root anchor — it IS the root, so we key on its
// own message id) and every follow-up inside the topic (which reports root_id =
// M) collapse to the same "chat:M" key and therefore the same agent session.
// Two distinct @-threads in one group root on different messages and stay
// separate, so topic isolation is preserved — this is the same
// root-of-the-thread model Slack already uses (channel:threadRoot; see
// engine.EnsureSessionInput). Keying on the topic id instead broke continuity,
// because the topic only exists AFTER the bot's first reply while the message
// that opened it was still top-level.
//
// Adapted from happyclaw's `rootId ?? messageId` session keying (MIT,
// riba2534/happyclaw). Pure function so the isolation contract is unit-tested
// without a DB.
func larkSessionRouting(msg channel.InboundMessage) (bindingKey string, config []byte) {
	chatID := msg.Source.ChatID
	if msg.Source.ChatType != channel.ChatTypeGroup {
		return chatID, nil
	}
	rootID := ""
	if msg.ReplyTo != nil {
		rootID = msg.ReplyTo.RootID
	}
	if rootID == "" {
		// A top-level @ has no root anchor: it is the root of the thread the
		// bot's reply_in_thread will open, so key on its own message id.
		rootID = msg.MessageID
	}
	cfg, _ := json.Marshal(larkBindingConfig{ChatID: chatID})
	return chatID + ":" + rootID, cfg
}

func (r *feishuSessionBinder) EnsureSession(ctx context.Context, p engine.EnsureSessionParams) (pgtype.UUID, error) {
	bindingKey, config := larkSessionRouting(p.Message)
	return r.session.EnsureSession(ctx, engine.EnsureSessionInput{
		WorkspaceID:    p.Installation.WorkspaceID,
		AgentID:        p.Installation.AgentID,
		InstallationID: p.Installation.ID,
		Sender:         p.Sender,
		BindingKey:     bindingKey,
		BindingConfig:  config,
		ChatType:       p.Message.Source.ChatType,
	})
}

func (r *feishuSessionBinder) MarkPendingFresh(ctx context.Context, sessionID pgtype.UUID, messageID string) error {
	return r.session.MarkPendingFresh(ctx, sessionID, messageID)
}

func (r *feishuSessionBinder) AppendMessage(ctx context.Context, p engine.AppendParams) (engine.AppendResult, error) {
	commandText := p.Message.CommandText
	if commandText == "" {
		commandText = p.Message.Text
	}
	return r.session.AppendUserMessage(ctx, engine.AppendInput{
		SessionID:           p.SessionID,
		Sender:              p.Sender,
		InstallationID:      p.InstallationID,
		Body:                p.Message.Text,
		CommandText:         commandText,
		MessageID:           p.Message.MessageID,
		ThreadID:            p.Message.Source.ThreadID,
		ClaimToken:          p.ClaimToken,
		MediaPendingSeconds: p.MediaPendingSeconds,
		ForceFresh:          p.Message.ForceFresh,
	})
}

func (r *feishuSessionBinder) BindMedia(ctx context.Context, p engine.BindMediaParams) (engine.BindMediaResult, error) {
	in := engine.BindMediaInput{
		MessageID:            p.MessageID,
		SessionID:            p.SessionID,
		WorkspaceID:          p.WorkspaceID,
		Sender:               p.Sender,
		IssueID:              p.IssueID,
		IssueDescriptionBase: p.IssueDescriptionBase,
		IssueCommandText:     p.IssueCommandText,
		Body:                 p.Body,
		MediaRefs:            p.MediaRefs,
	}
	if richer, ok := r.session.(interface {
		BindMediaRefsWithResult(context.Context, engine.BindMediaInput) (engine.BindMediaResult, error)
	}); ok {
		return richer.BindMediaRefsWithResult(ctx, in)
	}
	return engine.BindMediaResult{}, r.session.BindMediaRefs(ctx, in)
}

// ---- audit ----

type feishuAuditor struct{ audit AuditLogger }

func (r *feishuAuditor) RecordDrop(ctx context.Context, instID pgtype.UUID, msg channel.InboundMessage, reason engine.DropReason) error {
	// event_type is platform-specific (read from Raw); a decode failure is
	// non-fatal — the drop is still worth auditing without it.
	lm, _ := larkMsgFromRaw(msg)
	return r.audit.RecordDrop(ctx, AuditDropParams{
		InstallationID: instID,
		ChatID:         ChatID(msg.Source.ChatID),
		EventType:      lm.EventType,
		LarkEventID:    msg.EventID,
		LarkMessageID:  msg.MessageID,
		Reason:         DropReason(string(reason)),
	})
}

// ---- outbound replier ----

type feishuOutboundReplier struct{ replier OutcomeReplier }

func (r *feishuOutboundReplier) Reply(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, res engine.Result) {
	larkInst, ok := inst.Platform.(Installation)
	if !ok {
		return
	}
	lm, _ := larkMsgFromRaw(msg)
	r.replier.Reply(ctx, larkInst, lm, dispatchResultFromEngine(res))
}

// dispatchResultFromEngine maps the engine verdict to the Feishu DispatchResult
// the OutcomeReplier consumes. The Outcome/DropReason string values match 1:1.
func dispatchResultFromEngine(res engine.Result) DispatchResult {
	return DispatchResult{
		Outcome:            Outcome(string(res.Outcome)),
		DropReason:         DropReason(string(res.DropReason)),
		InstallationID:     res.InstallationID,
		ChatSessionID:      res.ChatSessionID,
		SenderOpenID:       OpenID(res.Sender),
		IssueID:            res.IssueID,
		IssueNumber:        res.IssueNumber,
		IssueIdentifier:    res.IssueIdentifier,
		IssueTitle:         res.IssueTitle,
		IssueDuplicate:     res.IssueDuplicate,
		IssueUsageHadMedia: res.IssueUsageHadMedia,
	}
}

// ---- typing indicator ----

type feishuTypingNotifier struct{ mgr *TypingIndicatorManager }

func (r *feishuTypingNotifier) OnIngested(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, sessionID pgtype.UUID) {
	larkInst, ok := inst.Platform.(Installation)
	if !ok {
		return
	}
	lm, _ := larkMsgFromRaw(msg)
	r.mgr.Add(ctx, larkInst, sessionID, msg.MessageID, lm.CreateTime)
}

// OnSettled clears the reaction when the run trigger enqueued no task (agent
// offline / archived, or an enqueue failure) — the Patcher's bus-driven clear on
// chat-done / task-failed never fires for those, so without this the Typing
// reaction sticks.
func (r *feishuTypingNotifier) OnSettled(ctx context.Context, sessionID pgtype.UUID) {
	r.mgr.Clear(ctx, sessionID)
}
