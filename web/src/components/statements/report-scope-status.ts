import type { FinancialReport } from "../../lib/financial-reports";

type ScopeStatus = {
  label: string;
  state: "neutral" | "unconfirmed" | "issue";
  explanation: string;
};

/** Summarize existing result checks for navigation without changing their meaning. */
export function reportScopeStatus(sources: readonly FinancialReport[]): ScopeStatus {
  if (!sources.length) return { label: "Review scope", state: "neutral", explanation: "Select a supported ledger report to review its scope." };
  if (sources.some(source => source.status === "unsupported")) {
    return { label: "Treatment unsupported", state: "issue", explanation: "A selected reporting treatment is unsupported. Open review scope for its limitations." };
  }
  const failures = sources.flatMap(source => source.checks || []).filter(check => check.status === "fail");
  if (failures.some(check => !["source_coverage", "opening_balances"].includes(check.id))) {
    return { label: "Report checks failed", state: "issue", explanation: "One or more report checks failed. Open review scope for the specific findings." };
  }
  if (failures.some(check => check.id === "source_coverage") || sources.some(source => source.scope.coverage === "partial")) {
    return { label: "Source incomplete", state: "issue", explanation: "The supplied source population is known to be incomplete. Open review scope for details." };
  }
  if (failures.some(check => check.id === "opening_balances") || sources.some(source => source.scope.opening_balances === "partial")) {
    return { label: "Opening balances incomplete", state: "issue", explanation: "Opening-balance coverage is incomplete. Open review scope for details." };
  }
  // The facts a professional reads first, stated plainly: basis, coverage and audit.
  // Nothing here is an alarm; the review panel lists every check behind them.
  const basis = sharedValue(sources.map(source => source.book.recognition_basis), { accrual: "Accrual", cash: "Cash", tax: "Tax" }, "not set");
  const coverage = sharedValue(sources.map(source => source.scope.coverage), { complete: "complete", confirmed: "complete" }, "not confirmed");
  const settled = basis !== "not set" && coverage === "complete" && !sources.some(source => source.status === "partial" || source.checks?.some(check => check.status === "unknown"));
  return {
    label: `Basis: ${basis} · Coverage: ${coverage} · Not audited`,
    state: settled ? "neutral" : "unconfirmed",
    explanation: settled
      ? "The basis and coverage are established. Calculation checks do not establish independent professional review. Open review scope for every check."
      : "Figures from the entries in this Book. Not every fact behind them is established yet. Open review scope for details.",
  };
}

/** One value when every source agrees, otherwise the open wording. */
function sharedValue(values: (string | undefined)[], known: Record<string, string>, open: string) {
  const words = new Set(values.map(value => (value && known[value]) || open));
  return words.size === 1 ? [...words][0] : open;
}
