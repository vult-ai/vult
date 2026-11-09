import assert from "node:assert/strict";
import test from "node:test";
import { React, withDOM, loadComponent } from "./helpers/dom.mjs";

test("searchable options stay in the modal scroll boundary, outside its scrolling body, and Escape retains the draft", async () => {
  await withDOM(async ({ render, window }) => {
    const { Dialog } = await loadComponent("src/components/ui/dialog.tsx");
    const { OnboardingChoice } = await loadComponent(
      "src/components/onboarding/onboarding-choice.tsx",
    );
    function View() {
      const [open, setOpen] = React.useState(true);
      const [value, setValue] = React.useState("UTC");
      return React.createElement(
        Dialog,
        { title: "Book setup", open, onOpenChange: setOpen },
        React.createElement("input", {
          name: "name",
          defaultValue: "Retained Book name",
        }),
        React.createElement(OnboardingChoice, {
          label: "Timezone",
          name: "accounting_timezone",
          value,
          onChange: setValue,
          items: [
            { id: "UTC", label: "UTC" },
            { id: "Europe/Warsaw", label: "Warsaw" },
          ],
        }),
      );
    }
    await render(React.createElement(View));
    await React.act(
      async () =>
        new Promise((resolve) => window.requestAnimationFrame(resolve)),
    );
    const dialog = document.querySelector('[role="dialog"]');
    const input = dialog.querySelector('input[role="combobox"]');
    await React.act(async () => input.click());
    const list = document.querySelector('[role="listbox"]');
    assert.ok(list);
    assert.equal(
      list.closest('[role="dialog"]'),
      dialog,
      "modal scroll lock includes the menu",
    );
    assert.equal(
      list.closest(".dialog-body"),
      null,
      "menu avoids the scrolling form's clipping",
    );
    assert.ok(list.closest(".vult-select-menu"));
    await React.act(async () =>
      input.dispatchEvent(
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
    assert.equal(document.querySelector('[role="listbox"]'), null);
    assert.equal(document.querySelector('[role="dialog"]'), dialog);
    assert.equal(dialog.dataset.state, "open");
    assert.equal(
      dialog.querySelector('[name="name"]').value,
      "Retained Book name",
    );
    assert.equal(
      dialog.querySelector('select[name="accounting_timezone"]').value,
      "UTC",
    );
  });
});

test("searchable choices outside a dialog retain their normal document portal", async () => {
  await withDOM(async ({ render }) => {
    const { OnboardingChoice } = await loadComponent(
      "src/components/onboarding/onboarding-choice.tsx",
    );
    await render(
      React.createElement(OnboardingChoice, {
        label: "Currency",
        value: "USD",
        onChange: () => {},
        items: [{ id: "USD", label: "US Dollar" }],
      }),
    );
    await React.act(async () =>
      document.querySelector('input[role="combobox"]').click(),
    );
    const list = document.querySelector('[role="listbox"]');
    assert.ok(list);
    assert.equal(list.closest('[role="dialog"]'), null);
    assert.equal(
      document.querySelector(".onboarding-choice").contains(list),
      false,
    );
  });
});

test("keyboard option navigation retains input focus and highlights the virtual option without committing it", async () => {
  await withDOM(async ({ render, window }) => {
    const { OnboardingChoice } = await loadComponent(
      "src/components/onboarding/onboarding-choice.tsx",
    );
    const changes = [];
    await render(
      React.createElement(OnboardingChoice, {
        label: "Timezone",
        name: "accounting_timezone",
        value: "UTC",
        onChange: (value) => changes.push(value),
        items: [
          { id: "UTC", label: "UTC" },
          { id: "Europe/Warsaw", label: "Warsaw" },
        ],
      }),
    );
    const input = document.querySelector('input[role="combobox"]');
    await React.act(async () => {
      input.focus();
      input.click();
    });
    await React.act(async () =>
      input.dispatchEvent(
        new window.KeyboardEvent("keydown", {
          key: "ArrowDown",
          bubbles: true,
          cancelable: true,
        }),
      ),
    );
    assert.equal(
      document.activeElement,
      input,
      "DOM focus remains on the search field",
    );
    const activeId = input.getAttribute("aria-activedescendant");
    assert.ok(activeId);
    const option = document.getElementById(activeId);
    assert.equal(option.getAttribute("role"), "option");
    assert.equal(
      option
        .querySelector("[data-select-option]")
        .getAttribute("data-highlighted"),
      "true",
    );
    assert.equal(
      option.contains(document.activeElement),
      false,
      "option focus is virtual",
    );
    assert.deepEqual(changes, []);
    assert.equal(
      document.querySelector('select[name="accounting_timezone"]').value,
      "UTC",
    );
  });
});
