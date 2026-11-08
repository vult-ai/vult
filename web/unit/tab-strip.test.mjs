import assert from "node:assert/strict";
import { createRequire } from "node:module";
import test from "node:test";
import { React, withDOM, loadComponent } from "./helpers/dom.mjs";

const { Tabs, TabPanel } = createRequire(import.meta.url)(
  "react-aria-components",
);
const items = [
  { id: "book", label: "Book" },
  { id: "preferences", label: "Preferences" },
  { id: "data", label: "Your data" },
];

test("the shared strip follows native selection without replacing its surface or mounted drafts", async () => {
  await withDOM(async ({ render, window }) => {
    const { TabStrip } = await loadComponent("src/components/ui/tab-strip.tsx");
    await render(
      React.createElement(
        Tabs,
        { defaultSelectedKey: "book" },
        React.createElement(TabStrip, { items, "aria-label": "Settings" }),
        ...items.map((item) =>
          React.createElement(
            TabPanel,
            { key: item.id, id: item.id, shouldForceMount: true },
            React.createElement("input", {
              "aria-label": `${item.label} draft`,
              defaultValue: "Original",
            }),
          ),
        ),
      ),
    );
    const strip = document.querySelector('[role="tablist"]');
    const tabs = [...document.querySelectorAll('[role="tab"]')];
    const draft = document.querySelector('input[aria-label="Book draft"]');
    draft.value = "Keep this unsaved value";
    assert.equal(strip.style.getPropertyValue("--vult-tab-count"), "3");
    assert.equal(strip.style.getPropertyValue("--vult-active-tab"), "0");

    for (const index of [2, 1, 2, 0]) {
      await React.act(async () => tabs[index].click());
      assert.equal(document.querySelector('[role="tablist"]'), strip);
      assert.equal(tabs[index].getAttribute("aria-selected"), "true");
      assert.equal(
        strip.style.getPropertyValue("--vult-active-tab"),
        String(index),
      );
      assert.equal(draft.isConnected, true);
      assert.equal(draft.value, "Keep this unsaved value");
    }

    await React.act(async () => {
      tabs[0].focus();
      tabs[0].dispatchEvent(
        new window.KeyboardEvent("keydown", {
          key: "ArrowRight",
          bubbles: true,
        }),
      );
    });
    assert.equal(tabs[1].getAttribute("aria-selected"), "true");
    assert.equal(tabs[1].hasAttribute("data-focus-visible"), true);
    assert.equal(document.activeElement, tabs[1]);
    assert.equal(draft.value, "Keep this unsaved value");
  });
});
