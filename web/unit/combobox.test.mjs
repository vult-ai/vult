import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

test("shared searchable selection commits one ID through the keyboard and submits one native form value", async () => {
  await withDOM(async ({ render, window }) => {
    const { ComboBox } = await loadComponent("src/components/ui/combobox.tsx");
    const changes = [];
    function Form() {
      const [value, setValue] = React.useState("USD");
      return React.createElement(
        "form",
        null,
        React.createElement(ComboBox, {
          label: "Currency",
          name: "currency",
          value,
          items: [
            { id: "USD", label: "US Dollar" },
            { id: "EUR", label: "Euro" },
          ],
          onChange: (next) => {
            changes.push(next);
            setValue(next);
          },
        }),
      );
    }
    await render(React.createElement(Form));
    const input = document.querySelector('input[role="combobox"]');
    const submitted = () => [
      ...new window.FormData(document.querySelector("form")).entries(),
    ];
    await React.act(async () => input.click());
    await React.act(async () => {
      Object.getOwnPropertyDescriptor(
        window.HTMLInputElement.prototype,
        "value",
      ).set.call(input, "Euro");
      input.dispatchEvent(new window.Event("input", { bubbles: true }));
    });
    assert.deepEqual(submitted(), [["currency", "USD"]]);
    assert.deepEqual(changes, []);
    assert.equal(document.querySelectorAll('[role="option"]').length, 1);
    for (const key of ["ArrowDown", "Enter"]) {
      await React.act(async () =>
        input.dispatchEvent(
          new window.KeyboardEvent("keydown", {
            key,
            bubbles: true,
            cancelable: true,
          }),
        ),
      );
    }
    assert.deepEqual(changes, ["EUR"]);
    assert.deepEqual(submitted(), [["currency", "EUR"]]);
    assert.equal(input.value, "Euro");
    assert.equal(document.activeElement, input);
  });
});

test("a resize that hides the search field dismisses its popup and preserves the committed value", async () => {
  await withDOM(async ({ render, window }) => {
    const { ComboBox } = await loadComponent("src/components/ui/combobox.tsx");
    const changes = [];
    await render(
      React.createElement(ComboBox, {
        label: "Currency",
        name: "currency",
        value: "USD",
        items: [
          { id: "USD", label: "US Dollar" },
          { id: "EUR", label: "Euro" },
        ],
        onChange: (value) => changes.push(value),
      }),
    );
    const input = document.querySelector('input[role="combobox"]');
    const trigger = input.closest('[role="group"]');
    let top = 50;
    trigger.getBoundingClientRect = () => new window.DOMRect(0, top, 300, 36);
    await React.act(async () => input.click());
    assert.equal(input.getAttribute("aria-expanded"), "true");
    await React.act(async () => {
      Object.getOwnPropertyDescriptor(
        window.HTMLInputElement.prototype,
        "value",
      ).set.call(input, "Euro");
      input.dispatchEvent(new window.Event("input", { bubbles: true }));
    });
    await React.act(async () => {
      top = window.innerHeight + 100;
      window.dispatchEvent(new window.Event("resize"));
    });
    await React.act(
      async () =>
        new Promise((resolve) => window.requestAnimationFrame(resolve)),
    );
    assert.equal(input.getAttribute("aria-expanded"), "false");
    assert.equal(input.value, "US Dollar");
    assert.equal(
      document.querySelector('select[name="currency"]').value,
      "USD",
    );
    assert.deepEqual(changes, []);
  });
});
