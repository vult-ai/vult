import assert from "node:assert/strict";
import test from "node:test";
import { React, withDOM, loadComponent } from "./helpers/dom.mjs";

const { checkToolbars } = await import("../scripts/check-toolbar-standard.mjs");
const { CollectionToolbar, CollectionTitle } = await loadComponent("src/design-system/collection-toolbar.tsx");

test("a table's toolbar must be the shared toolbar", () => {
  const files = {
    "a.tsx": "export function GoodToolbar() { return <CollectionToolbar lead={x} /> }",
    "b.tsx": "export function LooseToolbar() { return <div className=\"bar\">{x}</div> }",
  };
  assert.deepEqual(checkToolbars("x.tsx", "<Table toolbar={<CollectionToolbar lead={a} />} />", files), []);
  assert.deepEqual(checkToolbars("x.tsx", "<Table toolbar={<GoodToolbar />} />", files), []);
  assert.deepEqual(checkToolbars("x.tsx", "<Table toolbar={toolbar} />", files), []);
  assert.match(checkToolbars("x.tsx", "<Table toolbar={<LooseToolbar />} />", files).join(), /must render CollectionToolbar/);
  assert.match(checkToolbars("x.tsx", "<Table toolbar={<CollectionFilters items={[]} />} />", files).join(), /must render CollectionToolbar/);
});

test("the toolbar puts identity first and actions last, and counts read without parentheses", async () => {
  await withDOM(async ({ render }) => {
    await render(React.createElement(CollectionToolbar, {
      label: "Accounts",
      lead: React.createElement(CollectionTitle, { label: "Accounts", count: "212", meta: "USD", countLabel: "212 accounts" }),
      actions: React.createElement("button", null, "Manage"),
    }));
    const bar = document.querySelector(".vult-collection-toolbar");
    assert.deepEqual([...bar.children].map((c) => c.className), ["vult-collection-toolbar-lead", "vult-collection-toolbar-actions"]);
    assert.equal(bar.querySelector(".vult-collection-title").textContent, "Accounts212· USD");
    assert.ok(!/\(/.test(bar.textContent));
  });
});
