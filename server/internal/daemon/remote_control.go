package daemon

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"time"
)

// Remote control actions the server sends via the heartbeat-pending channel:
// restart the daemon, or return a snapshot of its own log. These mirror
// handleUpdate's report lifecycle (report a terminal status to the server) and
// reuse the daemon's existing self-restart machinery.

const (
	// gracefulRestartDrainTimeout bounds how long a graceful remote restart
	// waits for in-flight tasks before restarting anyway. Long enough for a
	// normal task to finish, short enough that a stuck task can't block a
	// requested restart forever.
	gracefulRestartDrainTimeout = 10 * time.Minute
	// remoteLogFetchMaxBytes caps how much of daemon.log we return so a huge log
	// can't blow up the report request or the UI.
	remoteLogFetchMaxBytes = 512 * 1024
)

// handleRestart restarts the daemon on server request. Graceful (the default)
// pauses new task claims and waits for in-flight tasks to finish before
// re-execing; Force restarts immediately. Desktop-managed daemons refuse, since
// Electron owns their lifecycle.
func (d *Daemon) handleRestart(ctx context.Context, runtimeID string, pending *PendingRestart) {
	if d.cfg.LaunchedBy == "desktop" {
		d.reportRuntimeCommandResult(ctx, runtimeID, pending.ID, map[string]any{
			"status": "failed",
			"error":  "daemon is managed by Multica Desktop — restart the Desktop app instead",
		})
		return
	}

	// Claim the same lifecycle barrier update/drain use so a restart can't race
	// a concurrent update. If another op owns it, report failure — the request
	// is already marked running server-side and won't be re-offered.
	if !d.tryBeginUpdate(false) {
		d.reportRuntimeCommandResult(ctx, runtimeID, pending.ID, map[string]any{
			"status": "failed",
			"error":  "another lifecycle operation (update/drain) is in progress",
		})
		return
	}
	keepBarrier := false
	defer func() {
		if !keepBarrier {
			d.releaseUpdate()
		}
	}()

	d.logger.Info("remote restart requested", "runtime_id", runtimeID, "command_id", pending.ID, "force", pending.Force)

	if !pending.Force {
		// The update barrier already pauses new claims; wait for the tasks that
		// were already in flight to finish, bounded so a stuck task can't block
		// the restart indefinitely.
		deadline := time.Now().Add(gracefulRestartDrainTimeout)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for d.activeTasks.Load() > 0 {
			if time.Now().After(deadline) {
				d.logger.Warn("remote restart: drain timed out; restarting with active tasks",
					"active", d.activeTasks.Load())
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}

	// Report completed BEFORE re-exec: triggerRestart cancels the root context,
	// so the report must land first. The actual come-back is observable via the
	// runtime's online status after it re-registers.
	d.reportRuntimeCommandResult(ctx, runtimeID, pending.ID, map[string]any{
		"status": "completed",
		"output": "Restart initiated",
	})

	d.triggerRestart()
	keepBarrier = d.RestartBinary() != ""
}

// handleLogFetch returns the tail of the daemon's own rotating daemon.log.
func (d *Daemon) handleLogFetch(ctx context.Context, runtimeID string, pending *PendingLogFetch) {
	d.logger.Info("remote log fetch requested", "runtime_id", runtimeID, "command_id", pending.ID, "lines", pending.Lines)
	if d.cfg.LogPath == "" {
		d.reportRuntimeCommandResult(ctx, runtimeID, pending.ID, map[string]any{
			"status": "failed",
			"error":  "no log file: the daemon is not running in background mode",
		})
		return
	}
	content, err := tailLogFileContent(d.cfg.LogPath, pending.Lines, remoteLogFetchMaxBytes)
	if err != nil {
		d.reportRuntimeCommandResult(ctx, runtimeID, pending.ID, map[string]any{
			"status": "failed",
			"error":  fmt.Sprintf("read log: %v", err),
		})
		return
	}
	d.reportRuntimeCommandResult(ctx, runtimeID, pending.ID, map[string]any{
		"status": "completed",
		"output": content,
	})
}

// reportRuntimeCommandResult posts a remote-command result to the server,
// reusing the update result's retry policy so a transient network blip doesn't
// drop the outcome.
func (d *Daemon) reportRuntimeCommandResult(ctx context.Context, runtimeID, commandID string, payload map[string]any) {
	d.reportUpdateResultWithRetry(ctx, runtimeID, commandID, func(ctx context.Context) error {
		return d.client.ReportRuntimeCommandResult(ctx, runtimeID, commandID, payload)
	})
}

// tailLogFileContent returns the last `lines` lines of the file at path, capped
// at maxBytes (from the end). A ring buffer keeps memory bounded regardless of
// file size. Returns an empty string (no error) for an empty file.
func tailLogFileContent(path string, lines, maxBytes int) (string, error) {
	if lines <= 0 {
		lines = 200
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	ring := make([]string, 0, lines)
	scanner := bufio.NewScanner(f)
	// Allow long log lines (JSON slog records can be large).
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if len(ring) == lines {
			ring = ring[1:]
		}
		ring = append(ring, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}

	// Assemble from the end, honoring the byte cap so a few enormous lines
	// can't exceed maxBytes.
	out := ""
	total := 0
	for i := len(ring) - 1; i >= 0; i-- {
		line := ring[i]
		if total+len(line)+1 > maxBytes && out != "" {
			break
		}
		if out == "" {
			out = line
		} else {
			out = line + "\n" + out
		}
		total += len(line) + 1
	}
	return out, nil
}
