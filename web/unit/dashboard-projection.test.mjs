import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent } from "./helpers/dom.mjs";

test("canonical dashboard adapter uses the API base once and preserves exact Book/date scope", async () => {
  const { loadDashboardProjection } = await loadComponent("src/lib/dashboard-projection.ts");
  const book = { id: "11111111-1111-4111-8111-111111111111",currency:"USD",scale:2 };
  const period = {from:"2026-10-01",to:"2026-10-31"};
  const payload = {book_id:book.id,period,performance:{status:"recorded",period,unit:"month",timezone:"UTC",buckets:[]},projection_contract:{version:2,book_id:book.id,event_kind:"accounting_recognition",precision:"date",currency:"USD",scale:2}};
  const original = globalThis.fetch; let requested;
  globalThis.fetch = async (url) => {requested=new URL(url,"http://localhost");return new Response(JSON.stringify(payload),{status:200});};
  try {
    const result = await loadDashboardProjection(book,period);
    assert.deepEqual(result.period,period);
    assert.equal(requested.pathname,"/api/v1/dashboard/projection");
    assert.equal(requested.searchParams.get("book_id"),book.id);
    assert.equal(requested.searchParams.get("from"),period.from);
    assert.equal(requested.searchParams.get("to"),period.to);
    assert.equal(requested.searchParams.get("view"),"recognition");
    for (const mismatch of [{book_id:"other"},{event_kind:"bank_posting"},{precision:"second"},{currency:"EUR"}]) {
      globalThis.fetch = async () => new Response(JSON.stringify({...payload,projection_contract:{...payload.projection_contract,...mismatch}}),{status:200});
      await assert.rejects(loadDashboardProjection(book,period),/accounting basis/);
    }
  } finally {globalThis.fetch=original;}
});
