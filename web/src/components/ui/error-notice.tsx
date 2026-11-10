import { useEffect, useId, useRef, useState, type MouseEvent } from "react";
import { useReadRetry } from "../../design-system/use-read-retry";
import { presentFailure, type FailureContext } from "../../lib/errors";
import { Button } from "./button";
import { Button as LinkButton } from "../../design-system/catalog/components/vult-ui/button";
import { FailureIcon } from "./failure-icon";
import { errorCopy } from "../../lib/error-copy";
import { continueSignInHref } from "../../lib/session";
import "./error-notice.css";

export type ErrorNoticeProps = FailureContext & {
  error?: unknown;
  onRetry?: () => void | Promise<unknown>;
  /** Read-only recovery. Never substitute a financial mutation retry here. */
  onReview?: () => void | Promise<unknown>;
  pending?: boolean;
};

/** One composed message/action surface; retries never dismiss it themselves. */
export function ErrorNotice({
  error,
  onRetry,
  onReview,
  pending = false,
  ...context
}: ErrorNoticeProps) {
  const messageId = useId();
  const [checking, setChecking] = useState(false);
  const [reviewFailure, setReviewFailure] = useState<{
    original: unknown;
    error: unknown;
  }>();
  const [retryFailure, setRetryFailure] = useState<{
    original: unknown;
    error: unknown;
  }>();
  const previous = useRef<unknown>(undefined);
  const retryTrigger = useRef<HTMLElement | null>(null);
  if (error) previous.current = error;
  const busy = pending || checking;
  if (!error && !busy) previous.current = undefined;
  const visible = error || (busy ? previous.current : undefined);
  const retryAction = useReadRetry(async (event: MouseEvent<HTMLButtonElement>) => {
    retryTrigger.current = event.currentTarget;
    setChecking(true);
    setRetryFailure(undefined);
    try {
      await onRetry?.();
    } catch (error) {
      setRetryFailure({ original: visible, error });
    } finally {
      setChecking(false);
    }
  }, busy);
  useEffect(() => {
    if (!visible && retryFailure) setRetryFailure(undefined);
  }, [visible, retryFailure]);
  useEffect(() => {
    if (reviewFailure && reviewFailure.original !== visible)
      setReviewFailure(undefined);
  }, [visible, reviewFailure]);
  useEffect(() => {
    if (busy || !retryTrigger.current) return;
    const trigger = retryTrigger.current;
    retryTrigger.current = null;
    // Disabled controls can lose focus. Never take it back from another control.
    if (trigger.isConnected && document.activeElement === document.body)
      trigger.focus();
  }, [busy]);
  if (!visible) return null;
  const baseFailure = presentFailure(
    retryFailure?.original === visible ? retryFailure.error : visible,
    context,
  );
  const failedReview =
    reviewFailure?.original === visible ? reviewFailure : undefined;
  const reviewCritical =
    failedReview &&
    presentFailure(failedReview.error, { intent: "read" }).severity ===
      "critical";
  const failure = reviewCritical
    ? {
        ...baseFailure,
        severity: "critical" as const,
        icon: "critical" as const,
        message: errorCopy.recordsUnverified,
      }
    : baseFailure;
  const retry =
    onRetry && context.intent === "read" && failure.recovery === "retry";
  const review =
    onReview &&
    (failure.recovery === "review" || failure.severity === "critical");
  return (
    <div
      className="error-notice"
      data-severity={failure.severity}
      data-state={busy ? "retrying" : "failed"}
    >
      <FailureIcon name={failure.icon} className="error-notice-icon" />
      <div
        className="error-notice-copy"
        id={messageId}
        role={
          failure.severity === "critical" || context.intent !== "read"
            ? "alert"
            : "status"
        }
        aria-atomic="true"
      >
        <p className="error-notice-description">
          {failedReview && !reviewCritical
            ? errorCopy.reviewUnavailable
            : failure.message}
        </p>
      </div>
      {(review || retry || failure.recovery === "sign-in" || failure.recovery === "confirm") && (
        <div className="error-notice-actions">
          {review ? (
            <Button
              color={failure.severity === "critical" ? "primary" : "tertiary"}
              size="sm"
              aria-describedby={messageId}
              busy={busy}
              onClick={async (event) => {
                retryTrigger.current = event.currentTarget;
                setChecking(true);
                setReviewFailure(undefined);
                try {
                  await onReview?.();
                } catch (error) {
                  setReviewFailure({ original: visible, error });
                } finally {
                  setChecking(false);
                }
              }}
            >
              Review
            </Button>
          ) : failure.recovery === "sign-in" ? (
            <LinkButton
              color="tertiary"
              size="sm"
              aria-describedby={messageId}
              href="/auth/login"
              target="_blank"
              rel="noopener noreferrer"
            >
              Sign in
            </LinkButton>
          ) : failure.recovery === "confirm" ? (
            <LinkButton
              color="tertiary"
              size="sm"
              aria-describedby={messageId}
              href={continueSignInHref()}
              // A notice sits beside work that may not be saved yet; confirm
              // in another tab so this one keeps it.
              target="_blank"
              rel="noopener noreferrer"
            >
              Continue
            </LinkButton>
          ) : (
            <Button
              color={failure.severity === "critical" ? "primary" : "tertiary"}
              size="sm"
              aria-describedby={messageId}
              busy={busy || retryAction.running}
              disabled={retryAction.coolingDown}
              onClick={retryAction.retry}
            >
              Try again
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

/** Validation text is associated with its input by the Field component. */
export function FieldError({ id, error }: { id?: string; error?: unknown }) {
  return error ? (
    <p id={id} className="field-error" role="alert">
      {presentFailure(error).message}
    </p>
  ) : null;
}
