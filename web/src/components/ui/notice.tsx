import { useEffect, type ReactNode } from "react";
import {
  AlertTriangle,
  CheckCircle,
  InfoCircle,
  XClose,
} from "../../design-system/icons";
import { announce } from "../../lib/announce";
import "./notice.css";

export type NoticeTone = "success" | "info" | "caution";

const icons = {
  success: CheckCircle,
  info: InfoCircle,
  caution: AlertTriangle,
} as const;

/**
 * Feedback that is not a failure: a completed change (success), something
 * worth knowing (info) or a condition to respect before acting (caution).
 * Failures use FailureState or ErrorMessage. Copy is one or two sentences,
 * each ending with a full stop.
 */
export function Notice({
  tone = "success",
  children,
  action,
  onDismiss,
  placement = "inline",
  announce: spoken = tone !== "caution",
  className = "",
  id,
}: {
  tone?: NoticeTone;
  children: string;
  action?: ReactNode;
  onDismiss?: () => void;
  placement?: "inline" | "floating";
  /** Persistent cautions are read with the page, not announced. */
  announce?: boolean;
  className?: string;
  id?: string;
}) {
  const Icon = icons[tone];
  useEffect(() => {
    if (spoken) announce(children);
  }, [children, spoken]);
  return (
    <div
      id={id}
      className={`vult-notice ${className}`.trim()}
      data-tone={tone}
      data-placement={placement}
    >
      <Icon className="vult-notice-icon" aria-hidden />
      <p className="vult-notice-text vult-type-ui">{children}</p>
      {action ? <div className="vult-notice-actions">{action}</div> : null}
      {onDismiss ? (
        <button
          type="button"
          className="vult-notice-dismiss"
          aria-label="Dismiss"
          onClick={onDismiss}
        >
          <XClose aria-hidden />
        </button>
      ) : null}
    </div>
  );
}
