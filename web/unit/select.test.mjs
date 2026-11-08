import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const options = [
  { value: "", label: "Choose explicitly…", disabled: true },
  { value: "income", label: "Income" },
  { value: "expense", label: "Expense" },
];

test("the canonical selector submits one native value, keeps its event target and resets uncontrolled forms", async () => {
  await withDOM(async ({ render, window }) => {
    const { Select } = await loadComponent("src/components/ui/select.tsx");
    const events = [];
    const view = (error) =>
      React.createElement(
        "form",
        null,
        React.createElement(Select, {
          "aria-label": "Type",
          name: "kind",
          defaultValue: "income",
          options,
          onChange: (event) =>
            events.push({
              target: event.target,
              currentTarget: event.currentTarget,
              value: event.currentTarget.value,
            }),
        }),
        error && React.createElement("p", { role: "alert" }, "Try again"),
      );
    await render(view(false));
    const control = document.querySelector('select[name="kind"]');
    control.value = "expense";
    await React.act(async () =>
      control.dispatchEvent(new window.Event("change", { bubbles: true })),
    );
    assert.deepEqual(events, [
      { target: control, currentTarget: control, value: "expense" },
    ]);
    assert.deepEqual(new window.FormData(control.form).getAll("kind"), [
      "expense",
    ]);
    assert.match(
      document.querySelector('button[aria-haspopup="listbox"]').textContent,
      /Expense/,
    );
    await render(view(true));
    assert.equal(document.querySelector('select[name="kind"]'), control);
    assert.equal(control.value, "expense");
    await React.act(async () => control.form.reset());
    assert.equal(control.value, "income");
    assert.match(
      document.querySelector('button[aria-haspopup="listbox"]').textContent,
      /Income/,
    );
    assert.equal(
      events.length,
      1,
      "reset must not synthesize an accounting change",
    );
  });
});

test("required validation blocks submission and focuses the visible selector; disabled selectors stay out of FormData", async () => {
  await withDOM(async ({ render, window }) => {
    const { Select } = await loadComponent("src/components/ui/select.tsx");
    const view = (disabled) =>
      React.createElement(
        "form",
        null,
        React.createElement(Select, {
          id: "purpose",
          label: "Purpose",
          name: "purpose",
          required: true,
          disabled,
          defaultValue: "",
          options,
          hint: "Choose the economic purpose",
        }),
      );
    await render(view(false));
    const control = document.querySelector('select[name="purpose"]');
    const button = document.querySelector('button[aria-haspopup="listbox"]');
    assert.equal(
      document.querySelector('label[for="purpose"]').control,
      button,
    );
    await React.act(async () =>
      assert.equal(control.form.reportValidity(), false),
    );
    assert.equal(document.activeElement, button);
    assert.equal(button.parentElement.getAttribute("data-invalid"), "true");
    assert.match(button.getAttribute("aria-describedby"), /purpose-error/);
    assert.equal(
      control.value,
      "",
      "placeholder must not silently choose a purpose",
    );
    assert.ok(document.getElementById("purpose-error").textContent);
    control.value = "income";
    await React.act(async () =>
      control.dispatchEvent(new window.Event("change", { bubbles: true })),
    );
    assert.equal(control.form.checkValidity(), true);
    assert.equal(document.getElementById("purpose-error"), null);
    await render(view(true));
    assert.equal(button.disabled, true);
    assert.equal(new window.FormData(control.form).has("purpose"), false);
  });
});

test("controlled selection remains owner-authoritative through vetoes and reset", async () => {
  await withDOM(async ({ render, window }) => {
    const { Select } = await loadComponent("src/components/ui/select.tsx");
    let target;
    await render(
      React.createElement(
        "form",
        null,
        React.createElement(Select, {
          "aria-label": "Active book",
          name: "book",
          value: "income",
          options,
          onChange: (event) => {
            target = event.currentTarget;
          },
        }),
      ),
    );
    const control = document.querySelector('select[name="book"]');
    control.value = "expense";
    await React.act(async () =>
      control.dispatchEvent(new window.Event("change", { bubbles: true })),
    );
    assert.equal(target, control);
    assert.equal(
      control.value,
      "income",
      "a dirty-book guard can reject a change",
    );
    await React.act(async () => control.form.reset());
    assert.equal(control.value, "income");
    assert.match(
      document.querySelector('button[aria-haspopup="listbox"]').textContent,
      /Income/,
    );
  });
});
