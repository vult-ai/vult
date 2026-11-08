import type { ReactNode, Ref } from "react";
import "./state.css";

export type StateScope = "page" | "region";

/**
 * Presentation shared by FailureState, EmptyState and LoadingState. Use those
 * components; this layout carries no copy, policy or behaviour of its own.
 */
export function StateLayout({
  kind,
  scope,
  tone = "quiet",
  icon,
  title,
  titleAs: Title = "p",
  titleId,
  titleRef,
  detail,
  detailId,
  actions,
  role,
  className,
  labelled = false,
  data,
}: {
  kind: "failure" | "empty" | "loading";
  scope: StateScope;
  tone?: "quiet" | "strong" | "critical";
  icon?: ReactNode;
  title: ReactNode;
  titleAs?: "h1" | "h2" | "h3" | "p";
  titleId?: string;
  titleRef?: Ref<HTMLHeadingElement & HTMLParagraphElement>;
  detail?: ReactNode;
  detailId?: string;
  actions?: ReactNode;
  role?: "alert" | "status";
  className?: string;
  /** Name the region by its title. Failures are landmarks; the rest are not. */
  labelled?: boolean;
  data?: Record<`data-${string}`, string | undefined>;
}) {
  const Root = labelled ? "section" : "div";
  return (
    <Root
      className={className ? `vult-state ${className}` : "vult-state"}
      data-kind={kind}
      data-scope={scope}
      data-tone={tone}
      aria-labelledby={labelled ? titleId : undefined}
      aria-describedby={labelled && detail ? detailId : undefined}
      aria-busy={kind === "loading" || undefined}
      {...data}
    >
      {icon && (
        <span className="vult-state-icon" aria-hidden="true">
          {icon}
        </span>
      )}
      <div
        className="vult-state-copy"
        role={role}
        aria-atomic={role ? true : undefined}
      >
        <Title
          ref={titleRef}
          id={titleId}
          tabIndex={Title === "p" ? undefined : -1}
          className={`vult-state-title ${
            scope === "page"
              ? "vult-type-section-title"
              : "vult-type-compact-title"
          }`}
        >
          {title}
        </Title>
        {detail && (
          <p id={detailId} className="vult-state-detail vult-type-ui">
            {detail}
          </p>
        )}
      </div>
      {actions && <div className="vult-state-actions">{actions}</div>}
    </Root>
  );
}
