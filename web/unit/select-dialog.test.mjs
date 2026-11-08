import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const options = [
  { value: "income", label: "Income" },
  { value: "expense", label: "Expense" },
];

test("nested selector dismissal retains the financial dialog and returns focus to its field", async () => {
  await withDOM(async ({ render, window }) => {
    const { Select } = await loadComponent("src/components/ui/select.tsx");
    const { Dialog } = await loadComponent("src/components/ui/dialog.tsx");
    function Fixture() {
      const [open, setOpen] = React.useState(true);
      return React.createElement(
        Dialog,
        { title: "Entry", open, onOpenChange: setOpen },
        React.createElement(
          "form",
          null,
          React.createElement("input", {
            name: "description",
            defaultValue: "Retained draft",
          }),
          React.createElement(Select, {
            "aria-label": "Type",
            name: "kind",
            defaultValue: "income",
            options,
          }),
        ),
      );
    }
    await render(React.createElement(Fixture));
    await React.act(
      async () =>
        new Promise((resolve) => window.requestAnimationFrame(resolve)),
    );
    const dialog = document.querySelector('[role="dialog"]');
    const button = dialog.querySelector('button[aria-haspopup="listbox"]');
    const draft = dialog.querySelector('[name="description"]');
    await React.act(async () => button.click());
    await React.act(
      async () =>
        new Promise((resolve) => window.requestAnimationFrame(resolve)),
    );
    assert.ok(document.querySelector('[role="listbox"]'));
    await React.act(async () =>
      document
        .querySelector('[role="option"][aria-selected="true"]')
        .dispatchEvent(
          new window.KeyboardEvent("keydown", {
            key: "Escape",
            bubbles: true,
            cancelable: true,
          }),
        ),
    );
    await React.act(
      async () =>
        new Promise((resolve) => window.requestAnimationFrame(resolve)),
    );
    assert.equal(
      document.querySelector('[role="listbox"]') === null,
      true,
      "selector closed",
    );
    assert.equal(
      document.querySelector('[role="dialog"]') === dialog,
      true,
      "dialog retained",
    );
    assert.equal(dialog.dataset.state, "open");
    assert.equal(
      document.activeElement === button,
      true,
      "focus returned to selector",
    );
    assert.equal(
      dialog.querySelector('[name="description"]') === draft,
      true,
      "draft retained",
    );
    assert.equal(draft.value, "Retained draft");
  });
});
