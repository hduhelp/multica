import { useCallback, useEffect, useRef, useState } from "react";
import { FileText, Loader2, RefreshCw, RotateCcw } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import type { RuntimeCommand } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";

const TERMINAL = new Set(["completed", "failed", "timeout"]);

interface DaemonControlSectionProps {
  runtimeId: string | null;
  isOnline: boolean;
  launchedBy?: string | null;
}

/**
 * Remote daemon controls: fetch a snapshot of the daemon's log and restart it.
 * Both flow through the server's runtime-command channel (POST then poll by id),
 * mirroring UpdateSection. Disabled when the runtime is offline or Desktop-managed.
 */
export function DaemonControlSection({
  runtimeId,
  isOnline,
  launchedBy,
}: DaemonControlSectionProps) {
  const { t } = useT("runtimes");
  const [logs, setLogs] = useState<string | null>(null);
  const [logsLoading, setLogsLoading] = useState(false);
  const [restarting, setRestarting] = useState(false);
  const [confirmRestart, setConfirmRestart] = useState(false);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const clearPoll = () => {
    if (pollRef.current) {
      clearInterval(pollRef.current);
      pollRef.current = null;
    }
  };
  useEffect(() => clearPoll, []);

  // runCommand starts a runtime command and resolves once it reaches a terminal
  // state (or rejects after ~90s of polling).
  const runCommand = useCallback(
    (start: () => Promise<RuntimeCommand>): Promise<RuntimeCommand> => {
      return new Promise((resolve, reject) => {
        start()
          .then((cmd) => {
            let tries = 0;
            clearPoll();
            pollRef.current = setInterval(async () => {
              tries += 1;
              try {
                const r = await api.getRuntimeCommand(runtimeId!, cmd.id);
                if (TERMINAL.has(r.status)) {
                  clearPoll();
                  resolve(r);
                } else if (tries > 90) {
                  clearPoll();
                  reject(new Error("timeout"));
                }
              } catch {
                // Ignore transient poll errors; the timeout above bounds it.
              }
            }, 1000);
          })
          .catch(reject);
      });
    },
    [runtimeId],
  );

  const viewLogs = async () => {
    if (!runtimeId) return;
    setLogsLoading(true);
    try {
      const r = await runCommand(() => api.fetchRuntimeLogs(runtimeId, 500));
      if (r.status === "completed") {
        setLogs(r.output || t(($) => $.daemon_control.logs_empty));
      } else {
        toast.error(r.error || t(($) => $.daemon_control.logs_failed));
      }
    } catch {
      toast.error(t(($) => $.daemon_control.logs_failed));
    } finally {
      setLogsLoading(false);
    }
  };

  const doRestart = async () => {
    setConfirmRestart(false);
    if (!runtimeId) return;
    setRestarting(true);
    try {
      const r = await runCommand(() => api.restartRuntime(runtimeId, false));
      if (r.status === "completed") {
        toast.success(t(($) => $.daemon_control.restart_initiated));
      } else {
        toast.error(r.error || t(($) => $.daemon_control.restart_failed));
      }
    } catch {
      toast.error(t(($) => $.daemon_control.restart_failed));
    } finally {
      setRestarting(false);
    }
  };

  const isManaged = launchedBy === "desktop";
  const disabled = !runtimeId || !isOnline || isManaged;

  return (
    <div className="space-y-3 border-t pt-4">
      <div className="flex items-center justify-between">
        <div>
          <p className="text-sm font-medium">
            {t(($) => $.daemon_control.title)}
          </p>
          <p className="text-xs text-muted-foreground">
            {t(($) => $.daemon_control.subtitle)}
          </p>
        </div>
      </div>

      <div className="flex flex-wrap gap-2">
        <Button
          variant="outline"
          size="sm"
          disabled={disabled || logsLoading}
          onClick={viewLogs}
        >
          {logsLoading ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <FileText className="h-3.5 w-3.5" />
          )}
          {t(($) => $.daemon_control.view_logs)}
        </Button>

        {confirmRestart ? (
          <div className="flex items-center gap-2">
            <Button
              variant="destructive"
              size="sm"
              disabled={restarting}
              onClick={doRestart}
            >
              {restarting ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <RotateCcw className="h-3.5 w-3.5" />
              )}
              {t(($) => $.daemon_control.restart_confirm)}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              disabled={restarting}
              onClick={() => setConfirmRestart(false)}
            >
              {t(($) => $.daemon_control.cancel)}
            </Button>
          </div>
        ) : (
          <Button
            variant="outline"
            size="sm"
            disabled={disabled || restarting}
            onClick={() => setConfirmRestart(true)}
          >
            <RotateCcw className="h-3.5 w-3.5" />
            {t(($) => $.daemon_control.restart)}
          </Button>
        )}
      </div>

      {isManaged && (
        <p className="text-xs text-muted-foreground">
          {t(($) => $.daemon_control.desktop_managed)}
        </p>
      )}

      {logs !== null && (
        <div className="space-y-1.5">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-muted-foreground">
              {t(($) => $.daemon_control.logs_title)}
            </span>
            <button
              type="button"
              className="text-muted-foreground hover:text-foreground disabled:opacity-50"
              disabled={logsLoading || disabled}
              onClick={viewLogs}
              aria-label={t(($) => $.daemon_control.logs_refresh)}
            >
              <RefreshCw
                className={`h-3.5 w-3.5 ${logsLoading ? "animate-spin" : ""}`}
              />
            </button>
          </div>
          <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-md border bg-muted/40 p-3 text-[11px] leading-relaxed font-mono">
            {logs}
          </pre>
        </div>
      )}
    </div>
  );
}
