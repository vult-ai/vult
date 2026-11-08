import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

test("canonical field adapters retain native events, economic dates, required fields and the receipt input across errors", async () => {
  await withDOM(async ({ render, window }) => {
    const { Field } = await loadComponent("src/components/form.tsx");
    let eventTarget;
    let selectTarget;
    const field = (label, element, hint) =>
      React.createElement(Field, { label, hint }, element);
    const view = (error) =>
      React.createElement(
        "form",
        null,
        field(
          "Amount",
          React.createElement("input", {
            name: "amount",
            defaultValue: "123.45",
            required: true,
            onInput: (e) => (eventTarget = e.currentTarget),
          }),
        ),
        field(
          "Economic date",
          React.createElement("input", {
            type: "date",
            name: "date",
            defaultValue: "2026-10-12",
            required: true,
          }),
        ),
        field(
          "Currency",
          React.createElement(
            "select",
            {
              name: "currency",
              defaultValue: "USD",
              onChange: (event) => (selectTarget = event.currentTarget),
            },
            React.createElement("option", { value: "USD" }, "US Dollar"),
            React.createElement("option", { value: "EUR" }, "Euro"),
          ),
          "Choose the original transaction currency",
        ),
        field(
          "Receipt",
          React.createElement("input", {
            type: "file",
            name: "receipt",
            accept: "image/png,application/pdf",
          }),
        ),
        error
          ? React.createElement("p", { role: "alert" }, "Retry this request")
          : null,
      );
    await render(view(false));
    const amount = document.querySelector('[name="amount"]');
    amount.dispatchEvent(new window.Event("input", { bubbles: true }));
    assert.equal(eventTarget, amount);
    assert.equal(amount.required, true);
    const select = document.querySelector('[name="currency"]');
    select.value = "EUR";
    await React.act(async () =>
      select.dispatchEvent(new window.Event("change", { bubbles: true })),
    );
    assert.equal(selectTarget, select);
    assert.equal(
      document.getElementById(select.getAttribute("aria-describedby"))
        .textContent,
      "Choose the original transaction currency",
      "the canonical select must retain its accessible hint",
    );
    const receipt = document.querySelector('[name="receipt"]');
    const files = [
      new window.File(["receipt"], "receipt.png", { type: "image/png" }),
    ];
    Object.defineProperty(receipt, "files", {
      configurable: true,
      value: files,
    });
    await render(view(true));
    assert.equal(
      document.querySelector('[name="receipt"]'),
      receipt,
      "an error must not replace the native receipt input",
    );
    assert.equal(receipt.files, files);
    assert.equal(receipt.accept, "image/png,application/pdf");
    const data = new window.FormData(document.querySelector("form"));
    assert.equal(data.get("amount"), "123.45");
    assert.equal(data.get("date"), "2026-10-12");
    assert.equal(data.get("currency"), "EUR");
    const label = document.querySelector(`label[for="${amount.id}"]`);
    assert.equal(label.textContent, "Amount");
  });
});

test("the canonical checkbox indicator keeps the controlled native change contract", async () => {
  await withDOM(async ({ render }) => {
    const { NativeCheckbox } = await loadComponent(
      "src/components/ui/native-checkbox.tsx",
    );
    let target;
    function Fixture() {
      const [checked, setChecked] = React.useState(false);
      return React.createElement(
        "label",
        null,
        React.createElement(NativeCheckbox, {
          checked,
          onChange: (event) => {
            target = event.currentTarget;
            setChecked(event.currentTarget.checked);
          },
        }),
        "Use actual amount",
      );
    }
    await render(React.createElement(Fixture));
    const input = document.querySelector('input[type="checkbox"]');
    await React.act(async () => input.click());
    assert.equal(target, input);
    assert.equal(input.checked, true);
    assert.equal(document.querySelector('input[type="checkbox"]'), input);
    await React.act(async () => input.click());
    assert.equal(input.checked, false);
  });
});
