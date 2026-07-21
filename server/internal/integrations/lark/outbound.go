package lark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// CardStatus mirrors lark_outbound_card_message.status. Kept as a typed
// alias so callers can't pass arbitrary strings into the status column.
type CardStatus string

const (
	CardStatusPending   CardStatus = "pending"
	CardStatusStreaming CardStatus = "streaming"
	CardStatusFinal     CardStatus = "final"
	CardStatusError     CardStatus = "error"
)

// CardKind enumerates the small set of card variants the patcher
// renders. The Renderer is plug-replaceable so the on-wire card
// template can evolve without touching the patcher's transport / DB
// logic.
type CardKind string

const (
	CardKindThinking CardKind = "thinking"
	CardKindRunning  CardKind = "running"
	CardKindFinal    CardKind = "final"
	CardKindError    CardKind = "error"
)

// CardRender is the rendered card body the Renderer produces. The
// patcher serializes the JSON before handing it to APIClient.
type CardRender struct {
	JSON string
}

// RenderInput is the (typed) snapshot the Renderer sees when building
// or patching a card. Fields are populated as they become available
// during a task lifecycle — IssueNumber is set for `/issue` flows,
// Content is set for completed chat tasks, ErrorMessage for failed.
type RenderInput struct {
	Kind         CardKind
	AgentName    string
	IssueNumber  int32
	IssueID      pgtype.UUID
	TaskID       pgtype.UUID
	Content      string
	ErrorMessage string
}

// Renderer turns a typed RenderInput into the actual Lark card JSON.
// Centralizing this lets us swap card templates (or A/B them) without
// touching event subscription or persistence code.
type Renderer interface {
	Render(in RenderInput) (CardRender, error)
}

// defaultRenderer produces minimal text-only cards that work against
// Lark's generic interactive-card schema. The exact JSON layout will
// be refined when the real product card design lands; this default
// keeps the wiring real (the JSON deserializes against Lark's schema)
// without committing the product to a particular template.
type defaultRenderer struct{}

// NewDefaultRenderer returns the production-default Renderer. Override
// via PatcherConfig.Renderer when a custom template is needed.
func NewDefaultRenderer() Renderer { return &defaultRenderer{} }

func (defaultRenderer) Render(in RenderInput) (CardRender, error) {
	// The production card is the status-coloured, markdown-rendering
	// schema-2.0 card in agent_reply_card.go. Keeping the Renderer indirection
	// lets tests substitute a stub and lets a future template swap in without
	// touching the patcher's transport / DB logic. update_multi is set inside
	// buildAgentReplyCard so a sent card stays patchable across the streaming
	// lifecycle.
	return buildAgentReplyCard(in)
}

// PatcherQueries is the narrow subset of *db.Queries the Patcher
// needs. Declared as an interface so the patcher is unit-testable
// without a real Postgres connection.
type PatcherQueries interface {
	GetAgentTask(ctx context.Context, id pgtype.UUID) (db.AgentTaskQueue, error)
	TaskHasChannelIngestedMessages(ctx context.Context, taskID pgtype.UUID) (bool, error)
	GetChatSession(ctx context.Context, id pgtype.UUID) (db.ChatSession, error)
	GetAgent(ctx context.Context, id pgtype.UUID) (db.Agent, error)
	GetLarkInstallation(ctx context.Context, id pgtype.UUID) (Installation, error)
	GetLarkChatSessionBindingBySession(ctx context.Context, chatSessionID pgtype.UUID) (ChatSessionBinding, error)
	GetLarkOutboundCardByTask(ctx context.Context, taskID pgtype.UUID) (OutboundCardMessage, error)
	CreateLarkOutboundCardMessage(ctx context.Context, arg CreateOutboundCardMessageParams) (OutboundCardMessage, error)
	UpdateLarkOutboundCardStatus(ctx context.Context, arg UpdateOutboundCardStatusParams) error
}

// CredentialsResolver decrypts an installation's app_secret for the
// transport layer. *InstallationService satisfies it directly; tests
// substitute a fake.
type CredentialsResolver interface {
	DecryptAppSecret(inst Installation) (string, error)
}

// PatcherConfig tunes the outbound Patcher. Defaults via withDefaults;
// tests typically override Renderer / Now / Logger.
type PatcherConfig struct {
	// Renderer drives the error card template used on the EventTaskFailed
	// path. The success path (EventChatDone) bypasses the renderer
	// entirely — it sends the raw assistant reply as a plain text IM
	// message — so this only matters for the failure branch.
	Renderer Renderer
	Now      func() time.Time
	Logger   *slog.Logger
}

func (c PatcherConfig) withDefaults() PatcherConfig {
	if c.Renderer == nil {
		c.Renderer = NewDefaultRenderer()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// Patcher reacts to task-lifecycle events on the event bus and forwards
// chat replies to Lark as plain text IM messages. It is the outbound
// side of §4.5 — but the original "thinking → streaming → final card"
// lifecycle was reduced to a single plain-text reply on EventChatDone
// after Bohan reported the card chrome made replies feel like system
// notifications. The error path is the one survivor of card rendering:
// failed runs surface as a short error card on EventTaskFailed because
// the visual distinction from a normal reply is genuinely useful.
//
// Scope:
//
//   - Only tasks whose chat_session has a lark_chat_session_binding
//     produce outbound. Tasks born from the web UI or autopilot pass
//     through unchanged.
//
//   - Each EventChatDone yields one Lark text message; there is no
//     streaming, no throttling, no DB row to track card-state.
//
//   - Multi-replica safety is inherited from the inbound WS lease: at
//     most one replica holds the installation lease at a time, the
//     event bus is per-process, so exactly one Patcher reacts per run.
type Patcher struct {
	queries         PatcherQueries
	credentials     CredentialsResolver
	client          APIClient
	typingIndicator *TypingIndicatorManager
	cfg             PatcherConfig
}

// NewPatcher constructs a Patcher bound to its dependencies. The
// patcher does not subscribe to the bus until Register is called.
func NewPatcher(queries PatcherQueries, credentials CredentialsResolver, client APIClient, cfg PatcherConfig) *Patcher {
	cfg = cfg.withDefaults()
	return &Patcher{
		queries:     queries,
		credentials: credentials,
		client:      client,
		cfg:         cfg,
	}
}

// SetTypingIndicatorManager wires the typing-indicator manager into the
// patcher so that replies clear the "processing" reaction before they
// are sent. Call once at boot after both the patcher and manager are
// constructed. Nil disables the clear step.
func (p *Patcher) SetTypingIndicatorManager(m *TypingIndicatorManager) {
	p.typingIndicator = m
}

// Register subscribes the patcher to the task-lifecycle events it
// cares about on the supplied bus. Idempotent only if you call it
// against a fresh bus; call sites should invoke it exactly once
// during server boot (after the bus + patcher are constructed and
// before HTTP traffic starts).
//
// Subscriptions are deliberately minimal:
//
//   - EventChatDone — the agent finished replying. The Patcher sends
//     the reply as a plain text IM message (Lark's `msg_type=text`),
//     not as an interactive card. The earlier card-based design (with
//     thinking → running → final patches) made every reply look like
//     a system notification nested in card chrome; flipping to plain
//     text makes free-form chat feel native.
//
//   - EventTaskFailed — the run failed; surface a short error card
//     so the failure is visually distinct from a successful reply.
//
// We deliberately do NOT subscribe to EventTaskQueued / EventTaskRunning
// (no thinking-card lifecycle anymore — adds noise without value) or to
// EventTaskCompleted (chat tasks always emit EventChatDone first, which
// is what we care about; non-chat tasks have no Lark binding anyway and
// would early-return). Leaving EventTaskCompleted unsubscribed also
// avoids the prior "Done." overwrite regression where the no-content
// EventTaskCompleted payload would wipe the real reply.
func (p *Patcher) Register(bus *events.Bus) {
	bus.Subscribe(protocol.EventTaskFailed, p.handleEvent)
	bus.Subscribe(protocol.EventChatDone, p.handleEvent)
}

func (p *Patcher) handleEvent(e events.Event) {
	// Use a fresh background ctx with a tight timeout: bus delivery is
	// synchronous so a stuck Lark HTTP call would otherwise wedge the
	// whole publish call site.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.processEvent(ctx, e); err != nil {
		p.cfg.Logger.Warn("lark patcher: event handling failed",
			"event_type", e.Type,
			"task_id", e.TaskID,
			"chat_session_id", e.ChatSessionID,
			"error", err,
		)
	}
}

func (p *Patcher) processEvent(ctx context.Context, e events.Event) error {
	taskID, chatSessionID, ok := taskAndSessionFromEvent(e)
	if !ok {
		return nil
	}
	if !chatSessionID.Valid {
		// Issue / autopilot tasks have no chat_session.
		return nil
	}
	binding, err := p.queries.GetLarkChatSessionBindingBySession(ctx, chatSessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Web-only chat session — not a Lark target.
			return nil
		}
		return fmt.Errorf("lookup chat session binding: %w", err)
	}

	// Only bound sessions reach here, so classify the task origin before
	// spending any send work. Web/mobile direct-chat tasks can reuse a session
	// that originated in Lark, but their replies belong only in Multica.
	// Sealed channel tasks own an input batch just like direct tasks, so the
	// discriminator is the immutable channel_ingested provenance of that
	// batch, not chat_input_task_id presence (which #5645 originally used).
	task, err := p.queries.GetAgentTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("load agent task: %w", err)
	}
	deliver, err := engine.TaskInputIsChannelIngested(ctx, p.queries, task)
	if err != nil {
		return fmt.Errorf("classify task input origin: %w", err)
	}
	if !deliver {
		return nil
	}

	inst, err := p.queries.GetLarkInstallation(ctx, binding.InstallationID)
	if err != nil {
		return fmt.Errorf("load installation: %w", err)
	}
	if InstallationStatus(inst.Status) != InstallationActive {
		// Revoked between trigger and event; nothing to patch.
		return nil
	}
	creds, err := p.installationCredentials(inst)
	if err != nil {
		return err
	}

	agent, agentErr := p.queries.GetAgent(ctx, inst.AgentID)
	agentName := ""
	if agentErr == nil {
		agentName = agent.Name
	}

	// Clear the "processing" reaction before the reply is visible so the
	// user sees a clean transition. Best-effort: a failure here is logged
	// but does not block the actual reply.
	if p.typingIndicator != nil {
		p.typingIndicator.Clear(ctx, chatSessionID)
	}

	switch e.Type {
	case protocol.EventChatDone:
		return p.sendChatReply(ctx, creds, binding, e.Payload)
	case protocol.EventTaskFailed:
		return p.fail(ctx, creds, binding, taskID, agentName, e.Payload)
	}
	return nil
}

// sendChatReply turns ChatDonePayload.Content into a Lark message.
// The wire shape is chosen per-reply based on whether the body
// contains any markdown syntax:
//
//   - Plain prose (no markdown) → `msg_type=text`. A one-line "Hi!"
//     reply should feel like a normal IM message, not a notification
//     card with chrome around it.
//
//   - Anything with markdown (headings, lists, code blocks, tables,
//     bold/italic, links) → schema-2.0 interactive card with a
//     `tag: "markdown"` body element so Lark's client renders the
//     formatting instead of leaving raw `**bold**` characters in
//     the transcript. The card is visually subtler than the legacy
//     binding-prompt template — just a single markdown block, no
//     header / icon / CTA buttons.
//
// Empty content is silently dropped: we'd rather show nothing than
// "Done." (the prior card fallback that confused Bohan in the live
// dev env). In practice an empty Content means the daemon completed
// the task without producing visible output, which only happens for
// edge cases like a chat task that just acknowledged a system event;
// not emitting a message there is the right product call.
func (p *Patcher) sendChatReply(ctx context.Context, creds InstallationCredentials, binding ChatSessionBinding, payload any) error {
	content := chatDoneContent(payload)
	if content == "" {
		return nil
	}
	// A finished reply should feel like a normal IM message, not a titled status
	// card. Plain prose goes out as msg_type=text; only markdown rides a
	// HEADERLESS schema-2.0 card so Lark renders the formatting. Neither shows a
	// header/title bar — the status header is reserved for the failure path
	// (fail), where the visual distinction earns its chrome. Both route through
	// threadReplyTarget so the reply threads off the user's message.
	target := threadReplyTarget(binding)
	if containsMarkdown(content) {
		return sendWithThreadFallback(p.cfg.Logger, "send markdown card", target, func(t ReplyTarget) error {
			_, err := p.client.SendMarkdownCard(ctx, SendMarkdownCardParams{
				InstallationID: creds,
				ChatID:         outboundChatID(binding),
				Markdown:       content,
				ReplyTarget:    t,
			})
			return err
		})
	}
	return sendWithThreadFallback(p.cfg.Logger, "send text message", target, func(t ReplyTarget) error {
		_, err := p.client.SendTextMessage(ctx, SendTextParams{
			InstallationID: creds,
			ChatID:         outboundChatID(binding),
			Text:           content,
			ReplyTarget:    t,
		})
		return err
	})
}

// sendAgentReply delivers one agent reply as the rich status card. The card is
// already table-safe (buildAgentReplyCard runs downgradeMarkdownTablesForLark),
// so — unlike happyclaw's post+md fallback (MIT, riba2534/happyclaw), which
// exists because raw interactive cards hit Lark's table-element cap — the send
// path does NOT retry with a different format when the card SEND fails: a blind
// re-send on an ambiguous "the server may have received it" error would
// duplicate the reply. The only fallback is for a card BUILD error (no send
// happened): degrade to a plain markdown/text send. Thread→chat-level downgrade
// for a classified thread-unsupported rejection is still handled inside
// sendWithThreadFallback.
func (p *Patcher) sendAgentReply(ctx context.Context, creds InstallationCredentials, chatID ChatID, target ReplyTarget, in RenderInput) error {
	card, err := p.cfg.Renderer.Render(in)
	if err != nil {
		p.cfg.Logger.Warn("lark: agent card build failed, sending plain reply", "error", err)
		return p.sendPlainReply(ctx, creds, chatID, target, cardBody(in))
	}
	return sendWithThreadFallback(p.cfg.Logger, "send agent card", target, func(t ReplyTarget) error {
		_, e := p.client.SendInteractiveCard(ctx, SendCardParams{
			InstallationID: creds,
			ChatID:         chatID,
			CardJSON:       card.JSON,
			ReplyTarget:    t,
		})
		return e
	})
}

// sendPlainReply is the card-build-failure degrade path: a markdown reply keeps
// its formatting via the schema-2.0 markdown card, plain prose goes out as a
// text message. Both route through sendWithThreadFallback.
func (p *Patcher) sendPlainReply(ctx context.Context, creds InstallationCredentials, chatID ChatID, target ReplyTarget, body string) error {
	if containsMarkdown(body) {
		return sendWithThreadFallback(p.cfg.Logger, "send markdown card", target, func(t ReplyTarget) error {
			_, e := p.client.SendMarkdownCard(ctx, SendMarkdownCardParams{
				InstallationID: creds,
				ChatID:         chatID,
				Markdown:       body,
				ReplyTarget:    t,
			})
			return e
		})
	}
	return sendWithThreadFallback(p.cfg.Logger, "send text message", target, func(t ReplyTarget) error {
		_, e := p.client.SendTextMessage(ctx, SendTextParams{
			InstallationID: creds,
			ChatID:         chatID,
			Text:           body,
			ReplyTarget:    t,
		})
		return e
	})
}

// outboundChatID recovers the real Lark chat id from the chat binding. The
// channel_chat_id may be a composite "chat:thread" topic-isolation key, so
// the real chat id is read from the binding config (larkBindingConfig);
// pre-topic rows (config "{}") route by the key itself, which for them IS the
// real chat id.
func outboundChatID(b ChatSessionBinding) ChatID {
	if len(b.Config) > 0 {
		var cfg larkBindingConfig
		if err := json.Unmarshal(b.Config, &cfg); err == nil && cfg.ChatID != "" {
			return ChatID(cfg.ChatID)
		}
	}
	return ChatID(b.ChannelChatID)
}

// threadReplyTarget derives the outbound reply target from the chat
// binding's most-recent inbound trigger. Adapted from happyclaw's Feishu
// routing (MIT, riba2534/happyclaw): the reply always anchors to the latest
// inbound message with reply_in_thread, so it quotes the user and opens (or
// continues) a Lark 话题:
//
//   - Normal group @mention → reply on last_lark_message_id with
//     reply_in_thread → the reply quotes the trigger and starts a topic.
//   - Inside a topic → the trigger already lives in the topic, so the same
//     reply_in_thread reply stays inside it.
//   - Session with no recorded trigger (last_lark_message_id empty) → the zero
//     ReplyTarget falls through to a fresh message.create.
//
// sendWithThreadFallback downgrades to a chat-level send if Lark rejects the
// threaded reply (topic recalled / disabled / aggregated), so a reply is never
// silently lost.
func threadReplyTarget(binding ChatSessionBinding) ReplyTarget {
	if binding.LastMessageID.Valid && binding.LastMessageID.String != "" {
		return ReplyTarget{MessageID: binding.LastMessageID.String, InThread: true}
	}
	return ReplyTarget{}
}

// sendWithThreadFallback runs send with the thread reply target and,
// ONLY when the threaded attempt fails with a Lark error that means the
// topic reply legitimately cannot land (trigger message recalled, topic
// gone, topics disabled, aggregated message — see
// threadReplyUnsupportedCodes), retries once at the chat level so the
// reply is not silently lost. Any other failure — transport error,
// 5xx, timeout, rate limit, or an ambiguous "the server may have
// received it" error — is logged and returned as a failure rather than
// retried: a blind chat-level retry could duplicate the reply or leak a
// thread-only reply into the main group chat. When target is already
// chat-level there is nothing to fall back to and the error is returned.
//
// It is a package-level function (rather than a Patcher method) so the
// event-driven Patcher and the immediate OutcomeReplier share one
// classified fallback path.
func sendWithThreadFallback(log *slog.Logger, op string, target ReplyTarget, send func(ReplyTarget) error) error {
	err := send(target)
	if err == nil {
		return nil
	}
	if target.IsSet() && isThreadReplyUnsupported(err) {
		log.Warn("lark: thread reply unsupported for target, retrying at chat level",
			"op", op, "reply_message_id", target.MessageID, "error", err)
		if fallbackErr := send(ReplyTarget{}); fallbackErr != nil {
			return fmt.Errorf("%s (chat-level fallback after thread-unsupported reply: %v): %w", op, err, fallbackErr)
		}
		return nil
	}
	if target.IsSet() {
		log.Warn("lark: thread reply failed; not falling back (non-classified error)",
			"op", op, "reply_message_id", target.MessageID, "error", err)
	}
	return fmt.Errorf("%s: %w", op, err)
}

func (p *Patcher) installationCredentials(inst Installation) (InstallationCredentials, error) {
	if p.credentials == nil {
		return InstallationCredentials{}, errors.New("lark patcher: credentials resolver missing")
	}
	secret, err := p.credentials.DecryptAppSecret(inst)
	if err != nil {
		return InstallationCredentials{}, fmt.Errorf("decrypt app_secret: %w", err)
	}
	creds := InstallationCredentials{
		AppID:     inst.AppID,
		AppSecret: secret,
		Region:    RegionOrDefault(inst.Region),
	}
	if inst.TenantKey.Valid {
		creds.TenantKey = inst.TenantKey.String
	}
	return creds, nil
}

// fail surfaces a short error card on task failure. Unlike the
// success path (plain text via sendChatReply), failures stay as cards
// because the user benefits from the visual distinction — a red /
// header-styled card is much harder to miss than a regular bubble,
// and these are rare enough that the card chrome isn't noisy.
//
// One-shot send (no patching, no DB row): if the task fails a second
// time we'd just send a second card, which is fine — failure is
// usually a single terminal event.
func (p *Patcher) fail(ctx context.Context, creds InstallationCredentials, binding ChatSessionBinding, taskID pgtype.UUID, agentName string, payload any) error {
	return p.sendAgentReply(ctx, creds, outboundChatID(binding), threadReplyTarget(binding), RenderInput{
		Kind:         CardKindError,
		AgentName:    agentName,
		TaskID:       taskID,
		ErrorMessage: errorMessageFromPayload(payload),
	})
}

// taskAndSessionFromEvent parses the typed-ish payload broadcastTaskEvent
// publishes — a map[string]any with `task_id` (always) and
// `chat_session_id` (chat tasks only). EventChatDone carries a
// ChatDonePayload struct instead.
func taskAndSessionFromEvent(e events.Event) (taskID, chatSessionID pgtype.UUID, ok bool) {
	if e.TaskID != "" {
		if err := taskID.Scan(e.TaskID); err != nil {
			taskID = pgtype.UUID{}
		}
	}
	if e.ChatSessionID != "" {
		if err := chatSessionID.Scan(e.ChatSessionID); err != nil {
			chatSessionID = pgtype.UUID{}
		}
	}
	switch p := e.Payload.(type) {
	case map[string]any:
		if !taskID.Valid {
			if s, _ := p["task_id"].(string); s != "" {
				_ = taskID.Scan(s)
			}
		}
		if !chatSessionID.Valid {
			if s, _ := p["chat_session_id"].(string); s != "" {
				_ = chatSessionID.Scan(s)
			}
		}
	case protocol.ChatDonePayload:
		if !taskID.Valid {
			_ = taskID.Scan(p.TaskID)
		}
		if !chatSessionID.Valid {
			_ = chatSessionID.Scan(p.ChatSessionID)
		}
	}
	return taskID, chatSessionID, taskID.Valid
}

func chatDoneContent(payload any) string {
	switch p := payload.(type) {
	case protocol.ChatDonePayload:
		return p.Content
	case map[string]any:
		if s, ok := p["content"].(string); ok {
			return s
		}
	}
	return ""
}

func errorMessageFromPayload(payload any) string {
	if m, ok := payload.(map[string]any); ok {
		if s, ok := m["error"].(string); ok {
			return s
		}
		if s, ok := m["error_message"].(string); ok {
			return s
		}
	}
	return ""
}
