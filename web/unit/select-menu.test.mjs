import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const options = [
  { value: "", label: "Choose explicitly…", disabled: true },
  { value: "income", label: "Income" },
  { value: "expense", label: "Expense" },
];

test("a canonical menu choice emits one input/change pair from the retained native field", async () => {
  await withDOM(async ({ render, window }) => {
    const { Select } = await loadComponent("src/components/ui/select.tsx");
    const events = [];
    await render(
      React.createElement(
        "form",
        null,
        React.createElement(Select, {
          "aria-label": "Type",
          name: "kind",
          defaultValue: "income",
          options,
          onInput: (event) =>
            events.push([
              "input",
              event.currentTarget,
              event.currentTarget.value,
            ]),
          onChange: (event) =>
            events.push([
              "change",
              event.currentTarget,
              event.currentTarget.value,
            ]),
        }),
      ),
    );
    const control = document.querySelector('select[name="kind"]');
    const button = document.querySelector('button[aria-haspopup="listbox"]');
    await React.act(async () => {
      // jsdom's click omits the browser's default pointer focus.
      button.focus();
      button.click();
    });
    const disabled = document.querySelector(
      '[role="option"][aria-disabled="true"]',
    );
    assert.ok(disabled, "the explicit placeholder remains disabled");
    await React.act(async () => disabled.click());
    assert.equal(events.length, 0);
    const expense = [...document.querySelectorAll('[role="option"]')].find(
      (item) => item.textContent === "Expense",
    );
    await React.act(async () => expense.click());
    assert.deepEqual(
      events.map(([type, target, value]) => [type, target === control, value]),
      [
        ["input", true, "expense"],
        ["change", true, "expense"],
      ],
    );
    assert.equal(
      document.querySelector('select[name="kind"]') === control,
      true,
      "native field retained",
    );
    assert.deepEqual(new FormData(control.form).getAll("kind"), ["expense"]);
    assert.equal(
      document.querySelector('[role="listbox"]') === null,
      true,
      "menu closed",
    );
    await React.act(async () => {
      // React Aria restores focus on a frame after its overlay cleanup.
      for (let frame = 0; frame < 10 && document.activeElement !== button; frame++)
        await new Promise((resolve) => window.requestAnimationFrame(resolve));
    });
    assert.equal(document.activeElement === button, true, "focus returned");
  });
});
