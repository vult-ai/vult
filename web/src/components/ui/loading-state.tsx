import { Loading01 } from "../../design-system/icons";
import { StateLayout, type StateScope } from "./state-layout";

export type LoadingStateProps = {
  /**
   * `page` stands in for a screen, `region` for a table, list or panel, and
   * `inline` for one line beside the content it is waiting for.
   */
  scope: StateScope | "inline";
  /**
   * One phrase ending in an ellipsis. “Opening …” for a Book or workspace,
   * “Loading …” for data, “Checking …” for verification, “Searching …” for
   * queries and “Saving …” for writes.
   */
  label: string;
};

/**
 * The one presentation for waiting. It appears only after a short delay so
 * fast reads never flash, and it sits where its failure state will appear.
 */
export function LoadingState({ scope, label }: LoadingStateProps) {
  if (scope === "inline")
    return (
      <p
        className="vult-state-inline vult-type-ui"
        data-kind="loading"
        role="status"
        aria-busy="true"
      >
        <Loading01 className="vult-state-spinner" aria-hidden="true" />
        <span>{label}</span>
      </p>
    );
  return (
    <StateLayout
      kind="loading"
      scope={scope}
      className="loading-state"
      icon={<Loading01 className="vult-state-spinner" />}
      title={label}
      role="status"
    />
  );
}
