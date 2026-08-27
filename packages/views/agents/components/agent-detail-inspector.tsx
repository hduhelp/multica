"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type {
  Agent,
  AgentRuntime,
  FixedRepoVcsType,
  MemberWithUser,
} from "@multica/core/types";
import {
  AGENT_DESCRIPTION_MAX_LENGTH,
  AGENT_MAX_CONCURRENT_TASKS_MAX,
  AGENT_MAX_CONCURRENT_TASKS_MIN,
} from "@multica/core/agents";
import {
  isRuntimeUsableForUser,
  runtimeModelsOptions,
} from "@multica/core/runtimes";
import { isImeComposing } from "@multica/core/utils";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { AvatarUploadControl } from "../../common/avatar-upload-control";
import {
  SettingsCard,
  SettingsRow,
  SettingsSaveState,
  SettingsSection,
} from "../../settings/components/settings-layout";
import { useAutoSave } from "../../settings/components/use-auto-save";
import { useT } from "../../i18n";
import { CharCounter } from "./char-counter";
import { ModelPicker } from "./inspector/model-picker";
import {
  buildModelChangeUpdate,
  type ModelCatalog,
} from "./inspector/model-change-cleanup";
import { RuntimePicker } from "./inspector/runtime-picker";
import { ThinkingSettingField } from "./inspector/thinking-prop-row";
import { ServiceTierSettingField } from "./inspector/service-tier-setting-field";

interface InspectorProps {
  agent: Agent;
  runtime: AgentRuntime | null;
  runtimes: AgentRuntime[];
  members: MemberWithUser[];
  currentUserId: string | null;
  canEdit: boolean;
  onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
}

interface ProfileDraft {
  name: string;
  description: string;
}

function profileDraftsEqual(left: ProfileDraft, right: ProfileDraft) {
  return left.name === right.name && left.description === right.description;
}

/**
 * Full-width General settings form. Every editable value is presented as an
 * explicit field; compact inspector chips are used only through their
 * settings-field variants, where the whole control is a visible click target.
 */
export function AgentDetailInspector({
  agent,
  runtime,
  runtimes,
  members,
  currentUserId,
  canEdit,
  onUpdate,
}: InspectorProps) {
  const { t } = useT("agents");
  const { t: ts } = useT("settings");
  const update = useCallback(
    (data: Record<string, unknown>) => onUpdate(agent.id, data),
    [agent.id, onUpdate],
  );

  const [name, setName] = useState(agent.name);
  const [description, setDescription] = useState(agent.description ?? "");

  useEffect(() => {
    setName(agent.name);
    setDescription(agent.description ?? "");
    // Reset only when moving to another agent. Cache updates from this form
    // must not erase a newer local draft while an autosave is in flight.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agent.id]);

  const profileDraft = useMemo(
    () => ({ name: name.trim(), description }),
    [description, name],
  );
  const savedProfile = useMemo(
    () => ({
      name: agent.name,
      description: agent.description ?? "",
    }),
    [agent.description, agent.name],
  );
  const saveProfile = useCallback(
    async (next: ProfileDraft) => {
      await update({ name: next.name, description: next.description });
    },
    [update],
  );
  const profileAutoSave = useAutoSave({
    value: profileDraft,
    savedValue: savedProfile,
    onSave: saveProfile,
    enabled:
      canEdit &&
      profileDraft.name.length > 0 &&
      profileDraft.description.length <= AGENT_DESCRIPTION_MAX_LENGTH,
    isEqual: profileDraftsEqual,
  });

  const isOnline = runtime?.status === "online";
  const canReadRuntime =
    runtime != null && isRuntimeUsableForUser(runtime, currentUserId);
  const canDiscoverRuntimeModels = isOnline && canReadRuntime;
  const nameInvalid = name.trim().length === 0;

  // Same query the Thinking / Speed fields already use, so switching model
  // costs no extra request. `null` = not authoritative (offline runtime, still
  // loading, or discovery failed) and must not trigger any clearing.
  const modelsQuery = useQuery(
    runtimeModelsOptions(canDiscoverRuntimeModels ? agent.runtime_id : null),
  );
  const modelCatalog = useMemo<ModelCatalog>(
    () =>
      modelsQuery.isSuccess
        ? modelsQuery.data.supported
          ? modelsQuery.data.models
          : []
        : null,
    [modelsQuery.data, modelsQuery.isSuccess],
  );
  const handleModelChange = useCallback(
    (model: string) =>
      update(
        buildModelChangeUpdate({
          provider: runtime?.provider ?? "",
          model,
          thinkingLevel: agent.thinking_level ?? "",
          serviceTier: agent.service_tier ?? "",
          catalog: modelCatalog,
        }),
      ),
    [agent.service_tier, agent.thinking_level, modelCatalog, runtime?.provider, update],
  );

  return (
    <div className="space-y-8">
      <SettingsSection
        title={t(($) => $.inspector.section_profile)}
        description={t(($) => $.inspector.section_profile_hint)}
        action={
          <SettingsSaveState
            status={profileAutoSave.status}
            savingLabel={ts(($) => $.auto_save.saving)}
            savedLabel={ts(($) => $.auto_save.saved)}
            errorLabel={ts(($) => $.auto_save.failed)}
          />
        }
      >
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.inspector.avatar_label)}
            description={t(($) => $.inspector.avatar_hint)}
            size="none"
          >
            <div className="flex justify-start sm:justify-end">
              <AvatarUploadControl
                variant="agent"
                value={agent.avatar_url ?? null}
                name={agent.name}
                size={56}
                disabled={!canEdit}
                onUploaded={(url) => update({ avatar_url: url })}
                onEmojiSelected={(value) => update({ avatar_url: value })}
              />
            </div>
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.inspector.name_label)}
            size="text"
          >
            <div>
              <Input
                type="text"
                name="agent-name"
                autoComplete="off"
                aria-label={t(($) => $.inspector.name_label)}
                value={name}
                onChange={(event) => setName(event.target.value)}
                onBlur={profileAutoSave.flush}
                disabled={!canEdit}
                aria-invalid={nameInvalid || undefined}
              />
              {nameInvalid ? (
                <p className="mt-1 text-caption text-destructive">
                  {t(($) => $.inspector.rename_required)}
                </p>
              ) : null}
            </div>
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.inspector.description_label)}
            size="text"
            align="start"
          >
            <div>
              <Textarea
                name="agent-description"
                autoComplete="off"
                aria-label={t(($) => $.inspector.description_label)}
                value={description}
                onChange={(event) => setDescription(event.target.value)}
                onBlur={profileAutoSave.flush}
                disabled={!canEdit}
                rows={5}
                maxLength={AGENT_DESCRIPTION_MAX_LENGTH}
                className="resize-y"
                placeholder={t(($) => $.inspector.description_placeholder)}
              />
              <CharCounter
                length={[...description].length}
                max={AGENT_DESCRIPTION_MAX_LENGTH}
              />
            </div>
          </SettingsRow>
        </SettingsCard>
      </SettingsSection>

      <SettingsSection
        title={t(($) => $.inspector.section_execution)}
        description={t(($) => $.inspector.section_execution_hint)}
      >
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.inspector.prop_runtime)}
            size="select-wide"
          >
            <RuntimePicker
              variant="field"
              showLabel={false}
              value={agent.runtime_id}
              runtimes={runtimes}
              members={members}
              currentUserId={currentUserId}
              canEdit={canEdit}
              // Model, thinking level, and service tier are runtime/model
              // native. Clear them together so the new runtime resolves its
              // own defaults instead of inheriting incompatible tokens.
              onChange={(id) =>
                update({
                  runtime_id: id,
                  model: "",
                  thinking_level: "",
                  service_tier: "",
                })
              }
            />
          </SettingsRow>
          <SettingsRow
            label={t(($) => $.inspector.prop_model)}
            size="select-wide"
          >
            <ModelPicker
              variant="field"
              showLabel={false}
              runtimeId={agent.runtime_id}
              runtimeOnline={canDiscoverRuntimeModels}
              value={agent.model ?? ""}
              canEdit={canEdit}
              onChange={handleModelChange}
            />
          </SettingsRow>
          <ThinkingSettingField
            label={t(($) => $.inspector.prop_thinking)}
            runtimeId={agent.runtime_id}
            runtimeOnline={canDiscoverRuntimeModels}
            provider={runtime?.provider ?? ""}
            model={agent.model ?? ""}
            value={agent.thinking_level ?? ""}
            canEdit={canEdit}
            onChange={(thinkingLevel) =>
              update({ thinking_level: thinkingLevel })
            }
          />
          <ServiceTierSettingField
            label={t(($) => $.inspector.prop_speed)}
            runtimeId={agent.runtime_id}
            runtimeOnline={canDiscoverRuntimeModels}
            provider={runtime?.provider ?? ""}
            model={agent.model ?? ""}
            value={agent.service_tier ?? ""}
            canEdit={canEdit}
            onChange={(serviceTier) => update({ service_tier: serviceTier })}
          />
          <SettingsRow
            label={t(($) => $.inspector.prop_concurrency)}
            size="select-wide"
          >
            <ConcurrencyField
              value={agent.max_concurrent_tasks}
              canEdit={canEdit}
              onSave={(next) => update({ max_concurrent_tasks: next })}
            />
          </SettingsRow>
        </SettingsCard>
      </SettingsSection>

      {/* Fixed repo mode is a local-runtime-only capability: the agent runs in
          a pre-existing directory on the daemon host instead of a per-task
          worktree. Hide the whole section for cloud runtimes where it cannot
          apply. */}
      {runtime?.runtime_mode === "local" && (
        <SettingsSection
          title={t(($) => $.inspector.section_fixed_repo)}
          description={t(($) => $.inspector.section_fixed_repo_hint)}
        >
          <SettingsCard>
            <FixedRepoSettings agent={agent} canEdit={canEdit} update={update} />
          </SettingsCard>
        </SettingsSection>
      )}
    </div>
  );
}

const FIXED_REPO_VCS_TYPES: FixedRepoVcsType[] = [
  "git",
  "perforce",
  "none",
  "custom",
];
const MAX_FIXED_REPO_PATHS = 16;

function pathsToText(paths: string[] | undefined): string {
  return (paths ?? []).join("\n");
}

function textToPaths(text: string): string[] {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line.length > 0);
}

/**
 * Fixed repo configuration: an enable toggle plus the path pool, VCS type, and
 * an (advisory) cleanup script. Only rendered for local runtimes. Each control
 * persists through the same `update` funnel as the rest of the inspector; the
 * path list and cleanup script commit on blur, the toggle and VCS select
 * commit immediately.
 */
function FixedRepoSettings({
  agent,
  canEdit,
  update,
}: {
  agent: Agent;
  canEdit: boolean;
  update: (data: Record<string, unknown>) => Promise<void>;
}) {
  const { t } = useT("agents");
  const enabled = agent.fixed_repo_enabled === true;
  // Local draft for the toggle. The server rejects fixed_repo_enabled=true with
  // empty fixed_repo_paths, so we can't persist the enable the moment the switch
  // flips — the paths field only appears afterwards. Instead we reveal the
  // config on the local draft and defer the enable write until paths exist,
  // committing fixed_repo_enabled and fixed_repo_paths together (see commitPaths).
  const [enabledDraft, setEnabledDraft] = useState(enabled);
  const [pathsDraft, setPathsDraft] = useState(
    pathsToText(agent.fixed_repo_paths),
  );
  const [cleanupDraft, setCleanupDraft] = useState(
    agent.fixed_repo_cleanup_script ?? "",
  );

  useEffect(() => {
    setEnabledDraft(enabled);
    setPathsDraft(pathsToText(agent.fixed_repo_paths));
    setCleanupDraft(agent.fixed_repo_cleanup_script ?? "");
  }, [agent.id, enabled, agent.fixed_repo_paths, agent.fixed_repo_cleanup_script]);

  const commitPaths = () => {
    const next = textToPaths(pathsDraft);
    // Normalize the textarea to the parsed form so the user sees exactly what
    // will be saved (blank lines / trailing spaces removed).
    setPathsDraft(next.join("\n"));
    // Never send an enable with empty paths — the server rejects it and the
    // required hint already tells the user what's missing.
    if (next.length === 0) return;
    const current = agent.fixed_repo_paths ?? [];
    const pathsChanged =
      next.length !== current.length ||
      next.some((p, i) => p !== current[i]);
    // First time paths become valid after toggling on: persist the enable and
    // paths in one write so the coupled invariant holds server-side.
    if (enabledDraft && !enabled) {
      void update({ fixed_repo_enabled: true, fixed_repo_paths: next });
    } else if (pathsChanged) {
      void update({ fixed_repo_paths: next });
    }
  };

  const onToggle = (checked: boolean) => {
    setEnabledDraft(checked);
    if (!checked) {
      // Disabling is always valid; persist immediately.
      void update({ fixed_repo_enabled: false });
    } else if (textToPaths(pathsDraft).length > 0) {
      // Re-enabling when paths are already filled in: persist right away.
      void update({
        fixed_repo_enabled: true,
        fixed_repo_paths: textToPaths(pathsDraft),
      });
    }
    // Otherwise wait for the user to enter paths; commitPaths finishes the enable.
  };

  const commitCleanup = () => {
    const trimmed = cleanupDraft.trim();
    const current = agent.fixed_repo_cleanup_script ?? "";
    if (trimmed === current) return;
    // Send null to clear the field so the server resets it (tri-state update).
    void update({ fixed_repo_cleanup_script: trimmed === "" ? null : trimmed });
  };

  const parsedCount = textToPaths(pathsDraft).length;

  return (
    <>
      <SettingsRow
        label={t(($) => $.inspector.prop_fixed_repo_enabled)}
        size="select-wide"
      >
        <Switch
          checked={enabledDraft}
          disabled={!canEdit}
          onCheckedChange={onToggle}
          aria-label={t(($) => $.inspector.prop_fixed_repo_enabled)}
        />
      </SettingsRow>

      {enabledDraft && (
        <>
          <SettingsRow
            label={t(($) => $.inspector.prop_fixed_repo_paths)}
            align="start"
          >
            <div>
              <Textarea
                value={pathsDraft}
                disabled={!canEdit}
                onChange={(event) => setPathsDraft(event.target.value)}
                onBlur={commitPaths}
                rows={4}
                spellCheck={false}
                placeholder={t(
                  ($) => $.inspector.fixed_repo_paths_placeholder,
                )}
                aria-label={t(($) => $.inspector.prop_fixed_repo_paths)}
                className="font-mono text-caption"
              />
              <p className="mt-1 text-caption text-muted-foreground">
                {t(($) => $.inspector.fixed_repo_paths_hint, {
                  max: MAX_FIXED_REPO_PATHS,
                })}
              </p>
              {parsedCount === 0 && (
                <p className="mt-1 text-caption text-destructive">
                  {t(($) => $.inspector.fixed_repo_paths_required)}
                </p>
              )}
              {parsedCount > MAX_FIXED_REPO_PATHS && (
                <p className="mt-1 text-caption text-destructive">
                  {t(($) => $.inspector.fixed_repo_paths_limit, {
                    max: MAX_FIXED_REPO_PATHS,
                  })}
                </p>
              )}
            </div>
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.inspector.prop_fixed_repo_vcs_type)}
            size="select-wide"
          >
            <Select
              items={FIXED_REPO_VCS_TYPES.map((vcs) => ({
                value: vcs,
                label: t(($) => $.inspector.fixed_repo_vcs_options[vcs]),
              }))}
              value={agent.fixed_repo_vcs_type ?? "git"}
              onValueChange={(next) =>
                next && update({ fixed_repo_vcs_type: next as FixedRepoVcsType })
              }
            >
              <SelectTrigger
                disabled={!canEdit}
                aria-label={t(($) => $.inspector.prop_fixed_repo_vcs_type)}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {FIXED_REPO_VCS_TYPES.map((vcs) => (
                  <SelectItem key={vcs} value={vcs}>
                    {t(($) => $.inspector.fixed_repo_vcs_options[vcs])}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </SettingsRow>

          {(agent.fixed_repo_vcs_type ?? "git") === "git" && (
            <SettingsRow
              label={t(($) => $.inspector.prop_fixed_repo_worktree)}
              align="start"
            >
              <div>
                <Switch
                  checked={agent.fixed_repo_worktree === true}
                  disabled={!canEdit}
                  onCheckedChange={(checked) =>
                    update({ fixed_repo_worktree: checked })
                  }
                  aria-label={t(($) => $.inspector.prop_fixed_repo_worktree)}
                />
                <p className="mt-1 text-caption text-muted-foreground">
                  {t(($) => $.inspector.fixed_repo_worktree_hint)}
                </p>
              </div>
            </SettingsRow>
          )}

          <SettingsRow
            label={t(($) => $.inspector.prop_fixed_repo_cleanup)}
            align="start"
          >
            <div>
              <Input
                value={cleanupDraft}
                disabled={!canEdit}
                onChange={(event) => setCleanupDraft(event.target.value)}
                onBlur={commitCleanup}
                spellCheck={false}
                placeholder={t(
                  ($) => $.inspector.fixed_repo_cleanup_placeholder,
                )}
                aria-label={t(($) => $.inspector.prop_fixed_repo_cleanup)}
                className="font-mono text-caption"
              />
              <p className="mt-1 text-caption text-muted-foreground">
                {t(($) => $.inspector.fixed_repo_cleanup_hint)}
              </p>
            </div>
          </SettingsRow>
        </>
      )}
    </>
  );
}

function ConcurrencyField({
  value,
  canEdit,
  onSave,
}: {
  value: number;
  canEdit: boolean;
  onSave: (next: number) => Promise<void>;
}) {
  const { t } = useT("agents");
  const [draft, setDraft] = useState(String(value));

  useEffect(() => setDraft(String(value)), [value]);

  const commit = () => {
    const next = Number(draft);
    if (
      !Number.isInteger(next) ||
      next < AGENT_MAX_CONCURRENT_TASKS_MIN ||
      next > AGENT_MAX_CONCURRENT_TASKS_MAX
    ) {
      setDraft(String(value));
      return;
    }
    if (next !== value) void onSave(next);
  };

  return (
    <div>
      <Input
        id="agent-concurrency"
        type="number"
        name="agent-concurrency"
        autoComplete="off"
        inputMode="numeric"
        min={AGENT_MAX_CONCURRENT_TASKS_MIN}
        max={AGENT_MAX_CONCURRENT_TASKS_MAX}
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        onBlur={commit}
        onKeyDown={(event) => {
          if (isImeComposing(event)) return;
          if (event.key === "Enter") {
            event.preventDefault();
            commit();
          }
        }}
        disabled={!canEdit}
        aria-label={t(($) => $.inspector.prop_concurrency)}
        className="font-mono tabular-nums"
      />
      <p className="mt-1 text-caption text-muted-foreground">
        {t(($) => $.pickers.concurrency_range, {
          min: AGENT_MAX_CONCURRENT_TASKS_MIN,
          max: AGENT_MAX_CONCURRENT_TASKS_MAX,
        })}
      </p>
    </div>
  );
}
