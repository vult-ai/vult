import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

const { checkSource, checkKinds, expected } = await import("../scripts/check-table-standard.mjs");
const sizing = await import("../src/design-system/column-layout.ts");

test("columns take their width from their kind", () => {
  for (const [source, rule] of [
    ['{ id: "date", label: "Date", kind: "date" as const, width: 144 }', /shared date width/],
    ['{ id: "amount", label: "Amount", kind: "money" as const, minWidth: 176 }', /shared money width/],
    ['{ id: "name", label: "Name", kind: "text" as const, minWidth: 100 }', /at least 120px/],
    ['{ id: "name", label: "Name", kind: "text" as const, grow: false }', /remove grow/],
  ])
    assert.match(checkSource("components/example.tsx", source).join("\n"), rule, source);
  assert.deepEqual(checkSource("components/example.tsx", '{ id: "name", label: `${currency} name`, kind: "text" as const, width: 320, minWidth: 190 }'), []);
  assert.deepEqual(checkSource("components/example.tsx", '{ id: "open", label: "File", kind: "control" as const, width: 112 }'), []);
});

test("the kind widths match the document", async () => {
  const table = await readFile(new URL("../src/design-system/catalog/components/application/table/records-table.tsx", import.meta.url), "utf8");
  assert.deepEqual(checkKinds(table), []);
  assert.match(checkKinds(table.replace("money: 160", "money: 208")).join(""), /160px/);
  const doc = await readFile(new URL("../../docs/design/tables.md", import.meta.url), "utf8");
  for (const value of Object.values(expected)) assert.match(doc, new RegExp(`${value}px`));
});

test("resizing a column changes only that column, at any position, and never below its minimum", () => {
  const columns = [
    { id: "date", min: 88, preferred: 112 },
    { id: "party", min: 120, preferred: 240 },
    { id: "amount", min: 96, preferred: 160 },
  ];
  const start = sizing.resolveColumnWidths(columns);
  assert.deepEqual(start, { date: 112, party: 240, amount: 160 }, "kinds give the starting widths");
  for (const [id, width] of [["date", 150], ["amount", 220], ["party", 300]]) {
    const next = sizing.resizeColumn(columns, start, id, width);
    assert.equal(next[id], width, `the ${id} column resizes`);
    for (const other of columns.filter((c) => c.id !== id)) assert.equal(next[other.id], start[other.id], `${other.id} does not move`);
  }
  assert.equal(sizing.resizeColumn(columns, start, "date", 10).date, 88);
  assert.equal(sizing.resolveColumnWidths(columns, { amount: 200 }).amount, 200, "a saved width is kept as is");
});
