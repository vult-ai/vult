import assert from "node:assert/strict";
import test from "node:test";
import { withDOM, loadComponent } from "./helpers/dom.mjs";

const write = await loadComponent("src/lib/book-settings-write.ts");
const book = { id: "11111111-1111-4111-8111-111111111111", name: "Northwind Studio", founder: "Ada", currency: "USD", scale: 2, start_date: "", metadata_revision: 3 };
const json = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

async function serving(answer, run) {
  const previous = globalThis.fetch;
  const sent = [];
  globalThis.fetch = async (url, options = {}) => {
    url = String(url);
    if (url.startsWith("/auth/session")) return json({ mode: "local", csrf: "csrf-token" });
    sent.push({ url, method: options.method || "GET", body: options.body });
    return answer(url, sent.length);
  };
  try { await withDOM(() => run(sent)); } finally { globalThis.fetch = previous; }
}

test("a settings change carries the Book's revision and a fresh key", () => {
  const a = write.settingsRequest(book, "Harbor", "Ada");
  const b = write.settingsRequest({ ...book, metadata_revision: undefined }, "Harbor", "Ada");
  assert.equal(a.revision, 3);
  assert.equal(b.revision, 0, "never revised is revision 0");
  assert.notEqual(a.idempotency_key, b.idempotency_key);
  assert.deepEqual(Object.keys(a).sort(), ["book_id", "founder", "idempotency_key", "name", "revision"]);
});

test("a lost answer stays unresolved, survives a reload, and is resent byte for byte", async () => {
  await serving((url, n) => (n === 1 ? json({ error: "unavailable" }, 503) : json({ ...book, name: "Harbor", metadata_revision: 4 })), async (sent) => {
    const request = write.settingsRequest(book, "Harbor", "Ada");
    const first = await write.sendSettings(request);
    assert.equal(first.status, "unresolved");
    assert.deepEqual(write.retainedSettingsRequest(book.id), request, "kept across a reload");
    const second = await write.sendSettings(first.request);
    assert.equal(second.status, "recorded");
    assert.equal(sent[0].body, sent[1].body, "the retry is the same request");
    assert.equal(write.retainedSettingsRequest(book.id), null);
  });
});

test("checking an unresolved change reads its outcome without resending it", async () => {
  const request = write.settingsRequest(book, "Harbor", "Ada");
  await serving((url, n) => (n === 1 ? json({ status: "unresolved" }) : json({ status: "recorded", result: { ...book, name: "Harbor", metadata_revision: 4 } })), async (sent) => {
    assert.equal((await write.checkSettings(request)).status, "unresolved");
    const done = await write.checkSettings(request);
    assert.equal(done.status, "recorded");
    assert.equal(done.book.name, "Harbor");
    assert.ok(sent.every((s) => s.method === "GET" && s.url.includes(`key=${request.idempotency_key}`)));
  });
});

test("a confirmed rejection releases the change; an unreadable success does not count as saved", async () => {
  await serving(() => json({ error: "conflict" }, 409), async () => {
    const outcome = await write.sendSettings(write.settingsRequest(book, "Harbor", "Ada"));
    assert.equal(outcome.status, "rejected");
    assert.equal(write.retainedSettingsRequest(book.id), null);
  });
  await serving(() => json({ ok: true }), async () => {
    const outcome = await write.sendSettings(write.settingsRequest(book, "Harbor", "Ada"));
    assert.equal(outcome.status, "unresolved");
  });
});
