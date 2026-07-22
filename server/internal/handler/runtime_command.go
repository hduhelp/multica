package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Runtime commands are one-shot control actions the server sends a live daemon
// through the heartbeat-pending channel — the same lifecycle as CLI update
// (frontend POST creates a pending request, daemon claims it on heartbeat,
// daemon reports a terminal result, UI polls by ID). Two kinds today:
//
//   - restart: the daemon restarts itself (graceful drain unless Force).
//   - logs:    the daemon returns the tail of its own daemon.log (snapshot).
//
// A single store backs both so multi-node deploys share one lifecycle. Restart
// reports "completed" the moment it commits to restarting (the actual come-back
// is observed via the runtime's online status), so both kinds flow through the
// same pending → running → completed/failed states.

type RuntimeCommandKind string

const (
	RuntimeCommandRestart RuntimeCommandKind = "restart"
	RuntimeCommandLogs    RuntimeCommandKind = "logs"
)

type RuntimeCommandStatus string

const (
	RuntimeCommandPending   RuntimeCommandStatus = "pending"
	RuntimeCommandRunning   RuntimeCommandStatus = "running"
	RuntimeCommandCompleted RuntimeCommandStatus = "completed"
	RuntimeCommandFailed    RuntimeCommandStatus = "failed"
	RuntimeCommandTimeout   RuntimeCommandStatus = "timeout"
)

// RuntimeCommand is a pending/terminal control action for a runtime.
type RuntimeCommand struct {
	ID              string               `json:"id"`
	RuntimeID       string               `json:"runtime_id"`
	InitiatorUserID string               `json:"-"`
	Kind            RuntimeCommandKind   `json:"kind"`
	Status          RuntimeCommandStatus `json:"status"`
	// Params
	Force bool `json:"force,omitempty"` // restart
	Lines int  `json:"lines,omitempty"` // logs
	// Result
	Output string `json:"output,omitempty"` // logs content / restart note
	Error  string `json:"error,omitempty"`

	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	RunStartedAt *time.Time `json:"-"`
}

const (
	runtimeCommandPendingTimeout = 60 * time.Second
	runtimeCommandRunningTimeout = 90 * time.Second
	runtimeCommandRetention      = 5 * time.Minute
	defaultLogFetchLines         = 200
	maxLogFetchLines             = 5000
)

func runtimeCommandTerminal(s RuntimeCommandStatus) bool {
	return s == RuntimeCommandCompleted || s == RuntimeCommandFailed || s == RuntimeCommandTimeout
}

// applyRuntimeCommandTimeout expires a request the daemon never picked up or
// never finished, so a dead daemon can't leave the UI polling forever.
func applyRuntimeCommandTimeout(req *RuntimeCommand, now time.Time) bool {
	switch req.Status {
	case RuntimeCommandPending:
		if now.Sub(req.CreatedAt) > runtimeCommandPendingTimeout {
			req.Status = RuntimeCommandTimeout
			req.Error = "daemon did not respond in time"
			req.UpdatedAt = now
			return true
		}
	case RuntimeCommandRunning:
		if req.RunStartedAt != nil && now.Sub(*req.RunStartedAt) > runtimeCommandRunningTimeout {
			req.Status = RuntimeCommandTimeout
			req.Error = "command did not complete in time"
			req.UpdatedAt = now
			return true
		}
	}
	return false
}

// RuntimeCommandStore is the lifecycle contract shared by the in-memory (single
// node) and Redis (multi-node) backends.
type RuntimeCommandStore interface {
	Create(ctx context.Context, cmd *RuntimeCommand) (*RuntimeCommand, error)
	Get(ctx context.Context, id string) (*RuntimeCommand, error)
	HasPending(ctx context.Context, runtimeID string) (bool, error)
	PopPending(ctx context.Context, runtimeID string) (*RuntimeCommand, error)
	Complete(ctx context.Context, id string, output string) error
	Fail(ctx context.Context, id string, errMsg string) error
}

type runtimeCommandError struct{ msg string }

func (e *runtimeCommandError) Error() string { return e.msg }

var errRuntimeCommandInProgress = &runtimeCommandError{msg: "a command is already in progress for this runtime"}

// ---------------------------------------------------------------------------
// In-memory store
// ---------------------------------------------------------------------------

type InMemoryRuntimeCommandStore struct {
	mu       sync.Mutex
	requests map[string]*RuntimeCommand
}

func NewInMemoryRuntimeCommandStore() *InMemoryRuntimeCommandStore {
	return &InMemoryRuntimeCommandStore{requests: make(map[string]*RuntimeCommand)}
}

func (s *InMemoryRuntimeCommandStore) Create(_ context.Context, cmd *RuntimeCommand) (*RuntimeCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for id, req := range s.requests {
		if now.Sub(req.CreatedAt) > runtimeCommandRetention {
			delete(s.requests, id)
		}
	}
	// One in-flight command per runtime keeps the daemon's heartbeat handling
	// unambiguous (it claims at most one of each kind).
	for _, req := range s.requests {
		if req.RuntimeID == cmd.RuntimeID && (req.Status == RuntimeCommandPending || req.Status == RuntimeCommandRunning) {
			return nil, errRuntimeCommandInProgress
		}
	}
	cmd.ID = randomID()
	cmd.Status = RuntimeCommandPending
	cmd.CreatedAt = now
	cmd.UpdatedAt = now
	s.requests[cmd.ID] = cmd
	return cmd, nil
}

func (s *InMemoryRuntimeCommandStore) Get(_ context.Context, id string) (*RuntimeCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.requests[id]
	if !ok {
		return nil, nil
	}
	applyRuntimeCommandTimeout(req, time.Now())
	return req, nil
}

func (s *InMemoryRuntimeCommandStore) HasPending(_ context.Context, runtimeID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, req := range s.requests {
		applyRuntimeCommandTimeout(req, now)
		if req.RuntimeID == runtimeID && req.Status == RuntimeCommandPending {
			return true, nil
		}
	}
	return false, nil
}

func (s *InMemoryRuntimeCommandStore) PopPending(_ context.Context, runtimeID string) (*RuntimeCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var oldest *RuntimeCommand
	for _, req := range s.requests {
		applyRuntimeCommandTimeout(req, now)
		if req.RuntimeID == runtimeID && req.Status == RuntimeCommandPending {
			if oldest == nil || req.CreatedAt.Before(oldest.CreatedAt) {
				oldest = req
			}
		}
	}
	if oldest != nil {
		oldest.Status = RuntimeCommandRunning
		started := now
		oldest.RunStartedAt = &started
		oldest.UpdatedAt = now
	}
	return oldest, nil
}

func (s *InMemoryRuntimeCommandStore) Complete(_ context.Context, id string, output string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req, ok := s.requests[id]; ok && !runtimeCommandTerminal(req.Status) {
		req.Status = RuntimeCommandCompleted
		req.Output = output
		req.UpdatedAt = time.Now()
	}
	return nil
}

func (s *InMemoryRuntimeCommandStore) Fail(_ context.Context, id string, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req, ok := s.requests[id]; ok && !runtimeCommandTerminal(req.Status) {
		req.Status = RuntimeCommandFailed
		req.Error = errMsg
		req.UpdatedAt = time.Now()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// initiateRuntimeCommand is the shared create path for the restart / logs POST
// handlers: it authorises the caller (owner/admin), builds the command, and
// stores it as pending for the daemon to claim on its next heartbeat.
func (h *Handler) initiateRuntimeCommand(w http.ResponseWriter, r *http.Request, cmd *RuntimeCommand) {
	runtimeID := chi.URLParam(r, "runtimeId")
	runtimeUUID, ok := parseUUIDOrBadRequest(w, runtimeID, "runtime_id")
	if !ok {
		return
	}
	rt, err := h.Queries.GetAgentRuntime(r.Context(), runtimeUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime not found")
		return
	}
	member, ok := h.requireWorkspaceMember(w, r, uuidToString(rt.WorkspaceID), "runtime not found")
	if !ok {
		return
	}
	if !canEditRuntime(member, rt) {
		writeError(w, http.StatusForbidden, "only runtime owners and workspace admins can control runtimes")
		return
	}
	cmd.RuntimeID = uuidToString(rt.ID)
	cmd.InitiatorUserID = uuidToString(member.UserID)
	created, err := h.RuntimeCommandStore.Create(r.Context(), cmd)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, created)
}

// RestartRuntime requests a remote daemon restart (protected; owner/admin).
func (h *Handler) RestartRuntime(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Force bool `json:"force"`
	}
	// Body is optional; default graceful.
	_ = json.NewDecoder(r.Body).Decode(&body)
	h.initiateRuntimeCommand(w, r, &RuntimeCommand{Kind: RuntimeCommandRestart, Force: body.Force})
}

// FetchRuntimeLogs requests a snapshot of the remote daemon's logs (protected).
func (h *Handler) FetchRuntimeLogs(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Lines int `json:"lines"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	lines := body.Lines
	if lines <= 0 {
		lines = defaultLogFetchLines
	}
	if lines > maxLogFetchLines {
		lines = maxLogFetchLines
	}
	h.initiateRuntimeCommand(w, r, &RuntimeCommand{Kind: RuntimeCommandLogs, Lines: lines})
}

// GetRuntimeCommand returns a command's status/result (protected; owner/admin or
// the initiator, so an in-flight poll survives a role change).
func (h *Handler) GetRuntimeCommand(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	runtimeUUID, ok := parseUUIDOrBadRequest(w, runtimeID, "runtime_id")
	if !ok {
		return
	}
	rt, err := h.Queries.GetAgentRuntime(r.Context(), runtimeUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime not found")
		return
	}
	member, ok := h.requireWorkspaceMember(w, r, uuidToString(rt.WorkspaceID), "runtime not found")
	if !ok {
		return
	}
	cmd, err := h.RuntimeCommandStore.Get(r.Context(), chi.URLParam(r, "commandId"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load command: "+err.Error())
		return
	}
	if cmd == nil || cmd.RuntimeID != uuidToString(rt.ID) {
		writeError(w, http.StatusNotFound, "command not found")
		return
	}
	if !canEditRuntime(member, rt) && cmd.InitiatorUserID != uuidToString(member.UserID) {
		writeError(w, http.StatusForbidden, "only runtime owners, workspace admins, and the initiator can view this command")
		return
	}
	writeJSON(w, http.StatusOK, cmd)
}

// ReportRuntimeCommandResult receives the daemon's terminal result (daemon-auth).
func (h *Handler) ReportRuntimeCommandResult(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	if _, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID); !ok {
		return
	}
	commandID := chi.URLParam(r, "commandId")
	existing, err := h.RuntimeCommandStore.Get(r.Context(), commandID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load command: "+err.Error())
		return
	}
	if existing == nil || existing.RuntimeID != runtimeID {
		writeError(w, http.StatusNotFound, "command not found")
		return
	}
	if runtimeCommandTerminal(existing.Status) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	var req struct {
		Status string `json:"status"` // completed | failed
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	switch req.Status {
	case "completed":
		_ = h.RuntimeCommandStore.Complete(r.Context(), commandID, req.Output)
	case "failed":
		_ = h.RuntimeCommandStore.Fail(r.Context(), commandID, req.Error)
	default:
		writeError(w, http.StatusBadRequest, "status must be completed or failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// pendingRuntimeCommandForHeartbeat pops the next pending command for a runtime
// and renders it as the matching heartbeat pending action. Returns nils when
// there is nothing to do.
func (h *Handler) pendingRuntimeCommandForHeartbeat(ctx context.Context, runtimeID string) (restart *protocol.DaemonHeartbeatPendingRestart, logs *protocol.DaemonHeartbeatPendingLogFetch) {
	if h.RuntimeCommandStore == nil {
		return nil, nil
	}
	has, err := h.RuntimeCommandStore.HasPending(ctx, runtimeID)
	if err != nil || !has {
		return nil, nil
	}
	cmd, err := h.RuntimeCommandStore.PopPending(ctx, runtimeID)
	if err != nil || cmd == nil {
		return nil, nil
	}
	switch cmd.Kind {
	case RuntimeCommandRestart:
		return &protocol.DaemonHeartbeatPendingRestart{ID: cmd.ID, Force: cmd.Force}, nil
	case RuntimeCommandLogs:
		return nil, &protocol.DaemonHeartbeatPendingLogFetch{ID: cmd.ID, Lines: cmd.Lines}
	}
	return nil, nil
}
