import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { useReadRetry } from "../../design-system/use-read-retry";
import {
  presentFailure,
  presentFailureState,
  type FailureContext,
} from "../../lib/errors";
import { failureStateCopy } from "../../lib/error-copy";
import { continueSignInHref } from "../../lib/session";
import { Button } from "./button";
import { Button as LinkButton } from "../../design-system/catalog/components/vult-ui/button";
import { FailureIcon } from "./failure-icon";
import { StateLayout } from "./state-layout";

export type FailureStateProps = FailureContext & {
  error: unknown;
  /**
   * `page` replaces a whole screen that could not render.
   * `region` replaces one panel, card or list inside a working page.
   * Failures beside the control that caused them use ErrorNotice instead.
   */
  scope: "page" | "region";
  /** Names what failed, e.g. "Couldn’t open Example Studio". */
  title?: string;
  /** Read retry. Shown only when the policy says retrying is safe. */
  onRetry?: () => void | Promise<unknown>;
  /** Read-only recovery. Never substitute a financial mutation here. */
  onReview?: () => void | Promise<unknown>;
  /** A quieter way forward, such as opening something else. */
  secondaryAction?: ReactNode;
  pending?: boolean;
};

/**
 * One anatomy for screens and regions that could not load: icon, title, one
 * supporting line and at most two actions. Severity sets emphasis; recovery
 * sets the action. Colour is reserved for verified integrity failures.
 */
export function FailureState({
  error,
  scope,
  title,
  onRetry,
  onReview,
  secondaryAction,
  pending = false,
  ...context
}: FailureStateProps) {
  const titleId = useId();
  const detailId = useId();
  const heading = useRef<HTMLHeadingElement & HTMLParagraphElement>(null);
  const [actionFailure, setActionFailure] = useState<{
    original: unknown;
    error: unknown;
  }>();
  const run = async (action?: () => void | Promise<unknown>) => {
    setActionFailure(undefined);
    try {
      await action?.();
    } catch (next) {
      setActionFailure({ original: error, error: next });
    }
  };
  const retry = useReadRetry(() => run(onRetry), pending);
  const review = useReadRetry(() => run(onReview), pending);
  const shown =
    actionFailure && actionFailure.original === error
      ? actionFailure.error
      : error;
  const state = presentFailureState(shown, { intent: "read", ...context });
  const reviewCritical =
    shown !== error &&
    presentFailure(shown, { intent: "read" }).severity === "critical";
  const severity = reviewCritical ? "critical" : state.severity;
  const detail = reviewCritical
    ? failureStateCopy.recordsUnverified
    : state.detail;
  const canReview =
    !!onReview && (state.recovery === "review" || severity === "critical");
  const canRetry =
    !!onRetry &&
    !canReview &&
    state.recovery === "retry" &&
    (context.intent ?? "read") === "read";
  const signIn = state.recovery === "sign-in";
  const confirm = state.recovery === "confirm";
  useEffect(() => {
    // A page that failed to load replaces what had focus. Only claim focus
    // when nothing else holds it.
    if (scope !== "page") return;
    const active = document.activeElement;
    if (!active || active === document.body)
      heading.current?.focus({ preventScroll: true });
  }, [scope]);
  const size = scope === "page" ? "md" : "sm";
  const busy = pending || retry.running || review.running;
  const actions =
    canReview || canRetry || signIn || confirm || secondaryAction ? (
      <>
        {canReview ? (
          <Button
            color={
              severity === "critical" || scope === "page"
                ? "primary"
                : "secondary"
            }
            size={size}
            aria-describedby={detailId}
            busy={busy}
            disabled={review.coolingDown}
            onClick={() => void review.retry()}
          >
            Review
          </Button>
        ) : canRetry ? (
          <Button
            color={scope === "page" ? "primary" : "secondary"}
            size={size}
            aria-describedby={detailId}
            busy={busy}
            disabled={retry.coolingDown}
            onClick={() => void retry.retry()}
          >
            Try again
          </Button>
        ) : signIn ? (
          <LinkButton
            color={scope === "page" ? "primary" : "secondary"}
            size={scope === "page" ? "md" : "sm"}
            aria-describedby={detailId}
            href="/auth/login"
            target="_blank"
            rel="noopener noreferrer"
          >
            Sign in
          </LinkButton>
        ) : confirm ? (
          <LinkButton
            color={scope === "page" ? "primary" : "secondary"}
            size={scope === "page" ? "md" : "sm"}
            aria-describedby={detailId}
            href={continueSignInHref()}
          >
            Continue
          </LinkButton>
        ) : null}
        {secondaryAction}
      </>
    ) : undefined;
  return (
    <StateLayout
      kind="failure"
      scope={scope}
      className="failure-state"
      labelled
      tone={
        severity === "critical"
          ? "critical"
          : severity === "attention"
            ? "strong"
            : "quiet"
      }
      icon={<FailureIcon name={reviewCritical ? "critical" : state.icon} />}
      title={title ?? state.title}
      titleAs={scope === "page" ? "h1" : "h2"}
      titleId={titleId}
      titleRef={heading}
      detail={detail}
      detailId={detailId}
      actions={actions}
      role={scope === "page" || severity === "critical" ? "alert" : "status"}
      data={{
        "data-severity": severity,
        "data-state": busy ? "retrying" : "failed",
      }}
    />
  );
}
