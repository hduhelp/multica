package scheduler

import (
	"context"
	"fmt"
	"time"
)

// JobNameRemoteSkillSync is the canonical name written to sys_cron_executions
// for the hourly URL-imported-skill refresh.
const JobNameRemoteSkillSync = "remote_skill_sync_hourly"

// RemoteSkillSyncJob re-syncs every URL-imported skill from its remote origin
// about once an hour, so a skill imported from a URL tracks upstream updates
// without a manual click. `sync` re-fetches each remote skill, rewrites the ones
// whose upstream changed, and returns how many changed.
//
// It runs on the global scope — one leaseholder does the whole set via the
// sys_cron_executions lease — and lives entirely outside the task-execution
// path, so keeping skills fresh never adds latency to an agent run.
func RemoteSkillSyncJob(sync func(context.Context) (int, error)) JobSpec {
	return JobSpec{
		Name:              JobNameRemoteSkillSync,
		Cadence:           1 * time.Hour,
		ScheduleDelay:     1 * time.Hour,
		CatchUpMode:       CatchUpLatestOnly,
		CatchUpWindow:     24 * time.Hour,
		RunTimeout:        10 * time.Minute,
		StaleTimeout:      15 * time.Minute,
		HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       3,
		RetryBackoff: []time.Duration{
			1 * time.Minute,
			5 * time.Minute,
			15 * time.Minute,
		},
		Scopes: StaticScopes(ScopeGlobal),
		Handler: func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
			if in.Heartbeat != nil {
				_ = in.Heartbeat(ctx)
			}
			changed, err := sync(ctx)
			if err != nil {
				return HandlerResult{}, fmt.Errorf("remote skill sync: %w", err)
			}
			return HandlerResult{
				RowsAffected: int64(changed),
				Result:       map[string]any{"skills_changed": changed},
			}, nil
		},
	}
}
