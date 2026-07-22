package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis-backed RuntimeCommandStore: same claim/report/poll lifecycle as the CLI
// update store, so a restart / log-fetch request created on one API replica is
// claimed and reported across others. Reuses claimPendingScript /
// deleteIfValueScript / runtimePendingRedisHashTag from the update store.

const (
	runtimeCmdKeyPrefix     = "mul:" + runtimePendingRedisHashTag + ":rtcmd:req:"
	runtimeCmdPendingPrefix = "mul:" + runtimePendingRedisHashTag + ":rtcmd:pending:"
	runtimeCmdActivePrefix  = "mul:" + runtimePendingRedisHashTag + ":rtcmd:active:"
	runtimeCmdPopMaxRetries = 5
)

func runtimeCmdKey(id string) string               { return runtimeCmdKeyPrefix + id }
func runtimeCmdPendingKey(runtimeID string) string { return runtimeCmdPendingPrefix + runtimeID }
func runtimeCmdActiveKey(runtimeID string) string  { return runtimeCmdActivePrefix + runtimeID }

type RedisRuntimeCommandStore struct {
	rdb *redis.Client
}

func NewRedisRuntimeCommandStore(rdb *redis.Client) *RedisRuntimeCommandStore {
	return &RedisRuntimeCommandStore{rdb: rdb}
}

type redisRuntimeCmdEnvelope struct {
	Public          *RuntimeCommand `json:"r"`
	RunStartedAt    *time.Time      `json:"s,omitempty"`
	InitiatorUserID string          `json:"u,omitempty"`
}

func (s *RedisRuntimeCommandStore) marshal(req *RuntimeCommand) ([]byte, error) {
	data, err := json.Marshal(redisRuntimeCmdEnvelope{
		Public: req, RunStartedAt: req.RunStartedAt, InitiatorUserID: req.InitiatorUserID,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal runtime command: %w", err)
	}
	return data, nil
}

func (s *RedisRuntimeCommandStore) unmarshal(raw []byte) (*RuntimeCommand, error) {
	var env redisRuntimeCmdEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode runtime command: %w", err)
	}
	if env.Public == nil {
		return nil, fmt.Errorf("decode runtime command: missing payload")
	}
	env.Public.RunStartedAt = env.RunStartedAt
	env.Public.InitiatorUserID = env.InitiatorUserID
	return env.Public, nil
}

func (s *RedisRuntimeCommandStore) Create(ctx context.Context, cmd *RuntimeCommand) (*RuntimeCommand, error) {
	now := time.Now()
	cmd.ID = randomID()
	cmd.Status = RuntimeCommandPending
	cmd.CreatedAt = now
	cmd.UpdatedAt = now
	data, err := s.marshal(cmd)
	if err != nil {
		return nil, err
	}
	activeKey := runtimeCmdActiveKey(cmd.RuntimeID)
	ok, err := s.rdb.SetNX(ctx, activeKey, cmd.ID, runtimeCommandRetention).Result()
	if err != nil {
		return nil, fmt.Errorf("reserve active command: %w", err)
	}
	if !ok {
		return nil, errRuntimeCommandInProgress
	}
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, runtimeCmdKey(cmd.ID), data, runtimeCommandRetention)
	pipe.ZAdd(ctx, runtimeCmdPendingKey(cmd.RuntimeID), redis.Z{Score: float64(now.UnixNano()), Member: cmd.ID})
	pipe.Expire(ctx, runtimeCmdPendingKey(cmd.RuntimeID), runtimeCommandRetention*2)
	if _, err := pipe.Exec(ctx); err != nil {
		_ = s.clearActiveIfMatches(ctx, cmd.RuntimeID, cmd.ID)
		_ = s.rdb.Del(ctx, runtimeCmdKey(cmd.ID)).Err()
		_ = s.rdb.ZRem(ctx, runtimeCmdPendingKey(cmd.RuntimeID), cmd.ID).Err()
		return nil, fmt.Errorf("persist runtime command: %w", err)
	}
	return cmd, nil
}

func (s *RedisRuntimeCommandStore) Get(ctx context.Context, id string) (*RuntimeCommand, error) {
	return s.load(ctx, id)
}

func (s *RedisRuntimeCommandStore) load(ctx context.Context, id string) (*RuntimeCommand, error) {
	raw, err := s.rdb.Get(ctx, runtimeCmdKey(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get runtime command: %w", err)
	}
	req, err := s.unmarshal(raw)
	if err != nil {
		return nil, err
	}
	if applyRuntimeCommandTimeout(req, time.Now()) {
		if err := s.persist(ctx, req); err != nil {
			return nil, err
		}
		_ = s.clearActiveIfMatches(ctx, req.RuntimeID, req.ID)
		s.rdb.ZRem(ctx, runtimeCmdPendingKey(req.RuntimeID), req.ID)
	}
	return req, nil
}

func (s *RedisRuntimeCommandStore) persist(ctx context.Context, req *RuntimeCommand) error {
	data, err := s.marshal(req)
	if err != nil {
		return err
	}
	if err := s.rdb.Set(ctx, runtimeCmdKey(req.ID), data, runtimeCommandRetention).Err(); err != nil {
		return fmt.Errorf("persist runtime command: %w", err)
	}
	return nil
}

func (s *RedisRuntimeCommandStore) HasPending(ctx context.Context, runtimeID string) (bool, error) {
	cnt, err := s.rdb.ZCard(ctx, runtimeCmdPendingKey(runtimeID)).Result()
	if err != nil {
		return false, fmt.Errorf("zcard pending commands: %w", err)
	}
	return cnt > 0, nil
}

func (s *RedisRuntimeCommandStore) PopPending(ctx context.Context, runtimeID string) (*RuntimeCommand, error) {
	pendingKey := runtimeCmdPendingKey(runtimeID)
	for attempt := 0; attempt < runtimeCmdPopMaxRetries; attempt++ {
		ids, err := s.rdb.ZRange(ctx, pendingKey, 0, 0).Result()
		if err != nil {
			return nil, fmt.Errorf("zrange pending commands: %w", err)
		}
		if len(ids) == 0 {
			return nil, nil
		}
		id := ids[0]
		req, err := s.load(ctx, id)
		if err != nil {
			return nil, err
		}
		if req == nil || req.Status != RuntimeCommandPending {
			s.rdb.ZRem(ctx, pendingKey, id)
			continue
		}
		now := time.Now()
		req.Status = RuntimeCommandRunning
		req.RunStartedAt = &now
		req.UpdatedAt = now
		data, err := s.marshal(req)
		if err != nil {
			return nil, err
		}
		result, err := claimPendingScript.Run(ctx, s.rdb,
			[]string{pendingKey, runtimeCmdKey(id)},
			id, data, int(runtimeCommandRetention.Seconds()),
		).Int64()
		if err != nil {
			return nil, fmt.Errorf("claim pending command: %w", err)
		}
		if result == 0 {
			continue
		}
		return req, nil
	}
	return nil, nil
}

func (s *RedisRuntimeCommandStore) Complete(ctx context.Context, id string, output string) error {
	return s.terminate(ctx, id, RuntimeCommandCompleted, output, "")
}

func (s *RedisRuntimeCommandStore) Fail(ctx context.Context, id string, errMsg string) error {
	return s.terminate(ctx, id, RuntimeCommandFailed, "", errMsg)
}

func (s *RedisRuntimeCommandStore) terminate(ctx context.Context, id string, status RuntimeCommandStatus, output, errMsg string) error {
	req, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if req == nil || runtimeCommandTerminal(req.Status) {
		return nil
	}
	req.Status = status
	req.Output = output
	req.Error = errMsg
	req.UpdatedAt = time.Now()
	if err := s.persist(ctx, req); err != nil {
		return err
	}
	_ = s.clearActiveIfMatches(ctx, req.RuntimeID, req.ID)
	s.rdb.ZRem(ctx, runtimeCmdPendingKey(req.RuntimeID), req.ID)
	return nil
}

func (s *RedisRuntimeCommandStore) clearActiveIfMatches(ctx context.Context, runtimeID, id string) error {
	if runtimeID == "" || id == "" {
		return nil
	}
	if err := deleteIfValueScript.Run(ctx, s.rdb, []string{runtimeCmdActiveKey(runtimeID)}, id).Err(); err != nil {
		return fmt.Errorf("clear active command: %w", err)
	}
	return nil
}
