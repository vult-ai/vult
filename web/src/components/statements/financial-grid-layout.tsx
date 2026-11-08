import type { ReactNode } from "react";
import { GRID_WIDTH_PREFERENCE_LIMIT } from "./financial-grid-widths";
import { ViewStateProvider, type ViewStateAdapter } from "../../design-system/catalog/components/vult-ui/view-state";
import { browserScopeKey } from "../../lib/user-preferences";
import { useOptionalPreferenceScope } from "../../lib/user-preferences-context";

export const STATEMENT_WIDTHS_KEY = "independent-columns-v1";

const adapter: ViewStateAdapter = {
  read(scope, name, storage) {
    const store = storage === "local" ? window.localStorage : window.sessionStorage;
    const value = store.getItem(`vult-statement-layout:v1:${scope}:${name}`);
    return value === null ? undefined : JSON.parse(value);
  },
  write(scope, name, value, storage) {
    const store = storage === "local" ? window.localStorage : window.sessionStorage;
    store.setItem(`vult-statement-layout:v1:${scope}:${name}`, JSON.stringify(value));
  },
};

/** Widths are personal browser presentation, never Book accounting settings. */
export function FinancialGridLayout({ book, report, children }: { book: string; report: string; children: ReactNode }) {
  const owner = useOptionalPreferenceScope();
  const scope = `${owner ? browserScopeKey(owner) : "memory"}:${book}:${report}`;
  return <ViewStateProvider key={scope} scope={scope} adapter={owner ? adapter : undefined}>{children}</ViewStateProvider>;
}

export const validStatementWidths = (value: unknown): value is Record<string, number> =>
  !!value && typeof value === "object" && !Array.isArray(value) && Object.entries(value).length <= 32 &&
  Object.entries(value).every(([key, width]) => key.length <= 100 && typeof width === "number" && Number.isFinite(width) && width >= 96 && width <= GRID_WIDTH_PREFERENCE_LIMIT);
