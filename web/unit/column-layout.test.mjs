import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const specs = [{ id: "name", min: 100, preferred: 200 }, { id: "amount", min: 128, preferred: 184, grow: false }, { id: "actions", min: 48, preferred: 48, fixed: true }];
const widths = () => JSON.parse(document.querySelector('[data-widths]').dataset.widths);
const handle = id => document.querySelector(`[data-resize-column="${id}"]`);
const pointer = async (window, id, type, x, pointerId = 7) => {
  const event = new window.MouseEvent(type, { bubbles: true, cancelable: true, button: 0, clientX: x });
  Object.defineProperty(event, "pointerId", { value: pointerId });
  await React.act(async () => handle(id).dispatchEvent(event));
};
const key = async (window, id, key, shiftKey = false) => React.act(async () => handle(id).dispatchEvent(new window.KeyboardEvent("keydown", { key, shiftKey, bubbles: true, cancelable: true })));
async function fixture(context) {
  const components = await loadComponent("unit/helpers/column-layout.tsx");
  const show = async ({ owner = "alice", book = "book-a", available = 600, view = "records", hidden = false, instance = "one" } = {}) => context.render(React.createElement(components.UserPreferencesProvider, { scope: { kind: "user", id: owner }, storage: null }, React.createElement(components.ColumnLayoutScope, { bookId: book }, React.createElement(components.ColumnHarness, { key: instance, available, view, hidden }))));
  await show();
  return { show, saved: () => Object.keys(context.window.localStorage).filter(key => key.startsWith("vult:column-layout:")) };
}

test("shared sizing preserves neighbors, financial minima, utilities and explicit Fit across a finite mixed batch", async () => {
  const sizing = await loadComponent("src/design-system/column-layout.ts");
  let before = sizing.resolveColumnWidths(specs);
  for (let step = 0; step < 600; step++) {
    const id = step % 2 ? "amount" : "name", next = sizing.resizeColumn(specs, before, id, (step * 137) % 5000 - 200);
    assert.equal(next.actions, 48);
    assert.equal(next[id === "name" ? "amount" : "name"], before[id === "name" ? "amount" : "name"]);
    assert.ok(next[id] >= specs.find(column => column.id === id).min && next[id] <= 4096);
    before = next;
  }
  const fit = sizing.fitColumnWidths(specs, sizing.resolveColumnWidths(specs), 600);
  assert.deepEqual(fit, { name: 368, amount: 184, actions: 48 });
  assert.equal(sizing.columnWidthTotal(specs, sizing.fitColumnWidths(specs, fit, 200)), 276);
  assert.equal(sizing.validColumnLayout({ name: Infinity }), false);
  assert.equal(sizing.validColumnLayout({ name: -1 }), false);
  assert.equal(sizing.validColumnLayout(JSON.parse('{"__proto__":123}')), false);
});

test("drag commits once, ignores other pointers, preserves neighbors and cancels without writing", async () => withDOM(async context => {
  const view = await fixture(context), initial = widths();
  let writes = 0;
  const set = context.window.Storage.prototype.setItem;
  context.window.Storage.prototype.setItem = function (...args) { writes++; return set.apply(this, args); };
  await pointer(context.window, "name", "pointerdown", 100);
  await pointer(context.window, "name", "pointermove", 160);
  await pointer(context.window, "amount", "pointerup", 260, 8);
  assert.deepEqual(widths(), { ...initial, name: 260 }); assert.equal(writes, 0);
  await key(context.window, "name", "Escape");
  assert.deepEqual(widths(), initial); assert.equal(writes, 0);
  await pointer(context.window, "name", "pointerdown", 100);
  await pointer(context.window, "name", "pointermove", 180);
  await pointer(context.window, "name", "pointerup", 180);
  assert.deepEqual(widths(), { ...initial, name: 280 }); assert.equal(writes, 1);
  await view.show({ available: 900 }); assert.deepEqual(widths(), { ...initial, name: 280 });
  await view.show({ instance: "reload" }); assert.deepEqual(widths(), { ...initial, name: 280 });
}));

test("width persistence isolates User, Book and table; hidden-column widths survive", async () => withDOM(async context => {
  const view = await fixture(context);
  await key(context.window, "name", "ArrowRight", true);
  await key(context.window, "amount", "ArrowRight");
  const saved = widths(); assert.deepEqual(saved, { name: 232, amount: 192, actions: 48 });
  await view.show({ hidden: true }); await key(context.window, "name", "ArrowRight");
  await view.show(); assert.deepEqual(widths(), { ...saved, name: 240 });
  for (const scope of [{ owner: "bob" }, { book: "book-b" }, { view: "sources" }]) { await view.show(scope); assert.equal(widths().name, 200); }
  await view.show(); assert.equal(widths().name, 240); assert.equal(view.saved().length, 1);
}));

test("keyboard bounds, individual reset, capture cancellation and viewport invalidation use the same controller", async () => withDOM(async context => {
  const view = await fixture(context);
  await key(context.window, "amount", "Home"); assert.equal(widths().amount, 128);
  await key(context.window, "amount", "End"); assert.equal(widths().amount, 4096);
  await key(context.window, "amount", "Enter"); assert.equal(widths().amount, 184);
  const before = widths();
  await pointer(context.window, "amount", "pointerdown", 100); await pointer(context.window, "amount", "pointermove", 200);
  await pointer(context.window, "amount", "lostpointercapture", 200); assert.deepEqual(widths(), before);
  await pointer(context.window, "name", "pointerdown", 100); await pointer(context.window, "name", "pointermove", 180);
  await view.show({ available: 400 }); assert.deepEqual(widths(), before);
  await pointer(context.window, "name", "pointerup", 180); assert.deepEqual(widths(), before);
}));

test("the canonical Columns menu fits explicitly, keeps fitted widths stable, and resets the layout", async () => withDOM(async context => {
  const view = await fixture(context);
  const choose = async label => {
    await React.act(async () => document.querySelector('button[aria-label="Columns"]').click());
    const item = [...document.querySelectorAll('[role="menuitem"]')].find(element => element.textContent.includes(label));
    assert.ok(item, `Missing ${label}`);
    await React.act(async () => item.click());
  };
  assert.equal(document.querySelectorAll("button button").length, 0);
  await choose("Fit columns"); assert.deepEqual(widths(), { name: 368, amount: 184, actions: 48 });
  await view.show({ available: 900 }); assert.equal(widths().name, 368);
  await choose("Reset column"); assert.deepEqual(widths(), { name: 200, amount: 184, actions: 48 });
  await view.show({ instance: "reset-reload" }); assert.equal(widths().name, 200);
}));

test("RecordsTable uses the shared controls without sorting or selecting during resize, and retains row behavior", async () => withDOM(async context => {
  const { RecordsTable } = await loadComponent("unit/helpers/column-layout.tsx");
  let sorts = 0, opened = 0;
  await context.render(React.createElement(RecordsTable, {
    ariaLabel: "Resize integration", testId: "resize-records", rows: [{ id: "one", name: "Example", amount: "$12.00" }],
    columns: [{ id: "name", label: "Name", kind: "text", width: 200, allowsSorting: true, isRowHeader: true }, { id: "amount", label: "Amount", kind: "money" }],
    rowId: row => row.id, rowLabel: row => row.name, renderCell: (row, column) => row[column.id], emptyState: "Empty", toolbar: "Records",
    onSortChange: () => sorts++, onRowAction: () => opened++,
  }));
  assert.equal(document.querySelectorAll('[data-resize-column]').length, 2);
  await pointer(context.window, "name", "pointerdown", 100); await pointer(context.window, "name", "pointerup", 180);
  // The first column also holds the 32px selection checkbox: 200 + 32 + 80.
  assert.equal(handle("name").getAttribute("aria-valuenow"), "312");
  assert.equal(handle("amount").getAttribute("aria-valuenow"), "160", "a money column starts at its kind width");
  // The last column resizes too, and moving it leaves the first where it is.
  await pointer(context.window, "amount", "pointerdown", 400); await pointer(context.window, "amount", "pointerup", 440);
  assert.equal(handle("amount").getAttribute("aria-valuenow"), "200");
  assert.equal(handle("name").getAttribute("aria-valuenow"), "312");
  await React.act(async () => handle("name").click());
  assert.equal(sorts, 0); assert.equal(opened, 0);
  assert.equal(document.querySelectorAll('input[type="checkbox"]:checked').length, 0);
  // A host grid may capture navigation keys before its cell's bubble handler.
  let intercepted = 0;
  const table = document.querySelector('[data-testid="resize-records"]');
  const navigate = event => { if (event.key === "ArrowRight") { intercepted++; event.stopPropagation(); } };
  table.addEventListener("keydown", navigate, true);
  await key(context.window, "name", "ArrowRight");
  assert.equal(handle("name").getAttribute("aria-valuenow"), "320");
  assert.equal(intercepted, 0);
  table.removeEventListener("keydown", navigate, true);
  await key(context.window, "name", "Enter");
  assert.equal(handle("name").getAttribute("aria-valuenow"), "232");
  await React.act(async () => document.querySelector('[data-record-id="one"] td').click());
  assert.equal(opened, 1);
}));

test("malformed or blocked storage cannot prevent resizing", async () => withDOM(async context => {
  const view = await fixture(context);
  const storedKey = "vult:column-layout:v1:alice:book-a:records";
  context.window.localStorage.setItem(storedKey, '{"name":-4}');
  await view.show({ instance: "malformed" }); assert.equal(widths().name, 200);
  context.window.Storage.prototype.setItem = () => { throw new Error("Blocked"); };
  await key(context.window, "name", "ArrowRight"); assert.equal(widths().name, 208);
  await key(context.window, "amount", "Home"); assert.equal(widths().amount, 128);
}));
