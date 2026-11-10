import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";
const { LoadingState } = await loadComponent(
  "src/components/ui/loading-state.tsx",
);
const { EmptyState } = await loadComponent("src/components/ui/empty-state.tsx");
const Icon = (props) => React.createElement("svg", props);

test("loading announces itself once, at every scope, without a heading", async () => {
  await withDOM(async ({ render }) => {
    for (const scope of ["page", "region", "inline"]) {
      await render(
        React.createElement(LoadingState, {
          scope,
          label: "Loading transactions…",
        }),
      );
      const status = document.querySelectorAll('[role="status"]');
      assert.equal(status.length, 1, scope);
      assert.equal(status[0].textContent, "Loading transactions…", scope);
      assert.ok(document.querySelector('[aria-busy="true"]'), scope);
      assert.equal(document.querySelector("h1, h2, h3"), null, scope);
    }
  });
});

test("empty states share the anatomy, stay quiet, and keep icons for nothing-yet states", async () => {
  await withDOM(async ({ render }) => {
    await render(
      React.createElement(EmptyState, {
        scope: "region",
        icon: Icon,
        title: "No transactions yet",
        detail: "Add a transaction from Manage.",
        action: React.createElement("button", null, "Add transaction"),
      }),
    );
    const state = document.querySelector(".vult-state.empty-state");
    assert.equal(state.dataset.kind, "empty");
    assert.ok(state.querySelector(".vult-state-icon svg"));
    assert.equal(
      state.querySelector(".vult-state-title").textContent,
      "No transactions yet",
    );
    assert.equal(state.querySelector("[role]"), null);
    assert.equal(state.querySelector("button").textContent, "Add transaction");
    await render(
      React.createElement(EmptyState, {
        scope: "region",
        title: "No matching transactions",
        detail: "Adjust the search or filters.",
      }),
    );
    assert.equal(document.querySelector(".vult-state-icon"), null);
    await render(
      React.createElement(EmptyState, {
        scope: "inline",
        title: "No sources linked",
      }),
    );
    assert.equal(
      document.querySelector(".vult-state-inline").textContent,
      "No sources linked",
    );
  });
});
