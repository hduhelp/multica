"use client";

import { useCallback, useState } from "react";
import { Loader2, ScrollText } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { api } from "@multica/core/api";
import type { AgentTask } from "@multica/core/types/agent";
import { AgentTranscriptDialog } from "./agent-transcript-dialog";
import { buildTimeline, type TimelineItem } from "./build-timeline";

interface TaskTranscriptButtonProps {
  /** The run/task whose transcript to open — a comment's source_task_id or a
   * chat assistant message's task_id. */
  taskId: string;
  agentName: string;
  className?: string;
  title?: string;
}

/**
 * Transcript icon for surfaces that only know a task id rather than a full
 * AgentTask — an agent comment (source_task_id) or a chat assistant message
 * (task_id). Unlike {@link TranscriptButton}, which the caller feeds a loaded
 * task, this hydrates the task metadata (GET /api/tasks/{id}) and its event
 * stream (listTaskMessages) in parallel on first click, then opens the shared
 * transcript dialog. Terminal tasks only: these surfaces render finished agent
 * output, so there is no live-cache subscription. A malformed/absent task
 * (api.getTask returns null) leaves the dialog closed rather than crashing the
 * surrounding view.
 */
export function TaskTranscriptButton({
  taskId,
  agentName,
  className,
  title = "View transcript",
}: TaskTranscriptButtonProps) {
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [task, setTask] = useState<AgentTask | null>(null);
  const [items, setItems] = useState<TimelineItem[]>([]);

  const handleClick = useCallback(
    (e: React.MouseEvent) => {
      e.preventDefault();
      e.stopPropagation();
      // Already hydrated: just reopen the cached transcript.
      if (task) {
        setOpen(true);
        return;
      }
      setLoading(true);
      Promise.all([api.getTask(taskId), api.listTaskMessages(taskId)])
        .then(([loadedTask, msgs]) => {
          if (!loadedTask) {
            console.error(`transcript: task ${taskId} unavailable`);
            return;
          }
          setTask(loadedTask);
          setItems(buildTimeline(msgs));
          setOpen(true);
        })
        .catch((err) => {
          console.error(err);
        })
        .finally(() => setLoading(false));
    },
    [task, taskId],
  );

  return (
    <>
      <Tooltip>
        <TooltipTrigger
          render={<button type="button" />}
          onClick={handleClick}
          disabled={loading}
          aria-label={title}
          className={cn(
            "flex items-center justify-center rounded p-1 text-muted-foreground hover:text-foreground hover:bg-accent/50 transition-colors disabled:opacity-50",
            className,
          )}
        >
          {loading ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <ScrollText className="h-3.5 w-3.5" />
          )}
        </TooltipTrigger>
        <TooltipContent>{title}</TooltipContent>
      </Tooltip>

      {open && task && (
        <AgentTranscriptDialog
          open={open}
          onOpenChange={setOpen}
          task={task}
          items={items}
          agentName={agentName}
          isLive={false}
        />
      )}
    </>
  );
}
