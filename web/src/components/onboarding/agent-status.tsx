import { useEffect, useState, type ReactNode } from "react";
import { Check } from "../../design-system/icons";
import "./agent-status.css";

export type AgentStatusStep = { id: string; label: string; detail?: string; durationMs?: number };

/** working: the agent is busy. waiting: the owner's move. done and paused end a run. */
export type AgentStatusState = "working" | "waiting" | "done" | "paused";

export function formatElapsed(ms: number) {
  const s = Math.max(0, Math.floor(ms / 1000));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`;
}

function useElapsed(since: number, running: boolean) {
  const [now, setNow] = useState(since);
  useEffect(() => {
    const tick = () => setNow(Date.now());
    const first = window.setTimeout(tick, 0);
    const timer = running ? window.setInterval(tick, 1000) : undefined;
    return () => {
      window.clearTimeout(first);
      window.clearInterval(timer);
    };
  }, [since, running]);
  return now - since;
}

/** What the agent has done and what is happening now: finished steps above,
 * the current one in a panel with its detail, timer and any action. */
export function AgentStatus({
  steps,
  label,
  detail,
  state,
  since,
  action,
  maxSteps = 4,
  children,
}: {
  steps: AgentStatusStep[];
  label: string;
  detail?: string;
  state: AgentStatusState;
  /** When the current step began; drives the timer while working. */
  since?: number;
  action?: ReactNode;
  maxSteps?: number;
  /** Extra guidance for the current step, shown under the panel. */
  children?: ReactNode;
}) {
  const recent = maxSteps > 0 ? steps.slice(-maxSteps) : [];
  const elapsed = useElapsed(since ?? 0, state === "working" && since !== undefined);
  return (
    <section className="agent-status" aria-label="Agent status" aria-busy={state === "working"}>
      {recent.length > 0 && (
        <ol className="agent-status-steps" aria-label="Finished steps">
          {recent.map((step) => (
            <li key={step.id}>
              <Check aria-label="Done" />
              <span className="agent-status-step-label">
                {step.label}
                {step.detail && <small>{step.detail}</small>}
              </span>
              {step.durationMs !== undefined && <time>{formatElapsed(step.durationMs)}</time>}
            </li>
          ))}
        </ol>
      )}
      <div className="agent-status-panel" data-state={state}>
        <span className="agent-status-indicator" aria-hidden="true">
          {state === "working" || state === "waiting" ? (
            <span className="agent-status-dots" data-state={state}>
              <span />
              <span />
              <span />
            </span>
          ) : (
            <span className="agent-status-final" data-state={state}>
              <Check />
            </span>
          )}
        </span>
        <span className="agent-status-text">
          <span className="agent-status-label" key={label}>
            {label}
          </span>
          {detail && (
            <span className="agent-status-detail" key={detail}>
              {detail}
            </span>
          )}
        </span>
        {state === "working" && since !== undefined && <time className="agent-status-time">{formatElapsed(elapsed)}</time>}
        {action}
      </div>
      {children && <div className="agent-status-more">{children}</div>}
      <p className="agent-status-live" role="status" aria-live="polite">
        {label}
      </p>
    </section>
  );
}
