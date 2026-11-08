import type { ComponentType, ReactNode } from "react";
import { StateLayout, type StateScope } from "./state-layout";

export type EmptyStateProps = {
  /**
   * `page` fills a screen with nothing in it yet; `region` fills a table,
   * list or inspector; `inline` is one quiet line inside a panel.
   */
  scope: StateScope | "inline";
  /**
   * Names what is missing: "No transactions yet" when nothing exists,
   * "No matching transactions" when filters hide everything. No full stop.
   */
  title: string;
  /** What to do next, as a sentence. Omit when there is nothing to do. */
  detail?: string;
  /**
   * Only for "nothing yet" states. Filtered and scoped results stay iconless
   * so they read as a result, not a place.
   */
  icon?: ComponentType<{ className?: string }>;
  action?: ReactNode;
};

/** The one presentation for “there is nothing here”. */
export function EmptyState({
  scope,
  title,
  detail,
  icon: Icon,
  action,
}: EmptyStateProps) {
  if (scope === "inline")
    return (
      <p className="vult-state-inline vult-type-ui" data-kind="empty">
        {detail ? `${title}. ${detail}` : title}
      </p>
    );
  return (
    <StateLayout
      kind="empty"
      scope={scope}
      className="empty-state"
      icon={Icon ? <Icon /> : undefined}
      title={title}
      detail={detail}
      actions={action}
    />
  );
}
