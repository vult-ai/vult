import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent } from "./helpers/dom.mjs";

const fixture = () => ({ status: "ready", book: { recognition_basis: "accrual" }, scope: { coverage: "complete", opening_balances: "confirmed" }, checks: [{ id: "balance_equation", status: "pass" }], digest: "unchanged-result", rows: [{ values: { current: "900719925474099300" } }] });

test("unconfirmed scope is distinct from known missing sources or opening balances", async () => {
  const { reportScopeStatus } = await loadComponent("src/components/statements/report-scope-status.ts");
  const unknown = { ...fixture(), status: "partial", checks: [{ id: "recognition_basis", status: "unknown" }] };
  assert.equal(reportScopeStatus([unknown]).label, "Basis: Accrual · Coverage: complete · Not audited");
  assert.equal(reportScopeStatus([unknown]).state, "unconfirmed");
  for (const [id, label] of [["source_coverage", "Source incomplete"], ["opening_balances", "Opening balances incomplete"]]) {
    assert.equal(reportScopeStatus([{ ...unknown, checks: [{ id, status: "fail" }] }]).label, label);
  }
  assert.equal(reportScopeStatus([{ ...fixture(), scope: { coverage: "partial", opening_balances: "confirmed" } }]).label, "Source incomplete");
  assert.equal(reportScopeStatus([{ ...fixture(), scope: { coverage: "complete", opening_balances: "partial" } }]).label, "Opening balances incomplete");
});

test("failed checks and unsupported treatment cannot be concealed by a ready base or unknown comparison", async () => {
  const { reportScopeStatus } = await loadComponent("src/components/statements/report-scope-status.ts");
  const base = fixture(), failed = { ...fixture(), status: "partial", checks: [{ id: "balance_equation", status: "fail" }, { id: "source_coverage", status: "fail" }] };
  assert.deepEqual(reportScopeStatus([base, failed]).label, "Report checks failed");
  assert.equal(reportScopeStatus([failed]).state, "issue");
  assert.equal(reportScopeStatus([base, { ...fixture(), status: "unsupported" }]).label, "Treatment unsupported");
});

test("settled facts read plainly, and an unset basis or coverage says so without claiming review", async () => {
  const { reportScopeStatus } = await loadComponent("src/components/statements/report-scope-status.ts");
  assert.equal(reportScopeStatus([fixture()]).label, "Basis: Accrual · Coverage: complete · Not audited");
  assert.equal(reportScopeStatus([fixture()]).state, "neutral");
  assert.equal(reportScopeStatus([]).label, "Review scope");
  assert.equal(reportScopeStatus([{ ...fixture(), book: { recognition_basis: "unknown" } }]).label, "Basis: not set · Coverage: complete · Not audited");
  assert.equal(reportScopeStatus([{ ...fixture(), book: {}, scope: { opening_balances: "confirmed" } }]).label, "Basis: not set · Coverage: not confirmed · Not audited");
  assert.equal(reportScopeStatus([fixture(), { ...fixture(), book: { recognition_basis: "cash" } }]).label, "Basis: not set · Coverage: complete · Not audited", "sources that disagree never show one basis");
});

test("presentation status never rewrites captured report checks, amounts or identities", async () => {
  const { reportScopeStatus } = await loadComponent("src/components/statements/report-scope-status.ts");
  const report = fixture(), original = JSON.stringify(report);
  for (const entry of report.checks) Object.freeze(entry);
  Object.freeze(report.checks); Object.freeze(report.book); Object.freeze(report.scope); Object.freeze(report.rows); Object.freeze(report);
  reportScopeStatus([report]);
  assert.equal(JSON.stringify(report), original);
});
