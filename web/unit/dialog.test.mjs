import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

test("workflow steps retain the modal frame and closing retains inert content until exit completes", async () => {
  await withDOM(async ({ render }) => {
    const { Dialog } = await loadComponent("src/components/ui/dialog.tsx");
    let control;
    function Fixture() {
      const [open, setOpen] = React.useState(true),
        [step, setStep] = React.useState("settings");
      control = { setOpen, setStep };
      return React.createElement(
        Dialog,
        { title: step, contentKey: step, open, onOpenChange: setOpen },
        React.createElement("input", {
          "aria-label": step,
          defaultValue: "Draft",
        }),
      );
    }
    await render(React.createElement(Fixture));
    const dialog = document.querySelector('[role="dialog"]');
    assert.ok(dialog);
    await React.act(async () => control.setStep("replacement"));
    assert.ok(
      document.querySelector('[role="dialog"]') === dialog,
      "The existing modal frame must stay mounted",
    );
    assert.equal(dialog.querySelector("h2").textContent, "replacement");
    let finish;
    const finished = new Promise((resolve) => {
      finish = resolve;
    });
    dialog.getAnimations = () =>
      dialog.dataset.state === "closed"
        ? [{ playState: "running", finished }]
        : [];
    await React.act(async () => control.setOpen(false));
    assert.ok(
      document.querySelector('[role="dialog"]') === dialog,
      "The existing modal frame must stay mounted",
    );
    assert.ok(dialog.hasAttribute("inert"));
    assert.equal(
      dialog.querySelector("input").getAttribute("aria-label"),
      "replacement",
    );
    await React.act(async () => control.setOpen(true));
    assert.ok(
      document.querySelector('[role="dialog"]') === dialog,
      "The existing modal frame must stay mounted",
    );
    assert.ok(!dialog.hasAttribute("inert"));
    const tick = () =>
      new Promise((done) =>
        requestAnimationFrame(() => requestAnimationFrame(done)),
      );
    await React.act(async () => {
      finish();
      await tick();
    });
    assert.ok(
      document.querySelector('[role="dialog"]') === dialog,
      "The existing modal frame must stay mounted",
    );
    dialog.getAnimations = () => [];
    await React.act(async () => control.setOpen(false));
    await React.act(async () => {
      await tick();
    });
    assert.ok(
      !document.querySelector('[role="dialog"]'),
      "The closed frame must unmount after its exit completes",
    );
  });
});

test("an in-flight form owns pending dismissal and releases it on settlement", async () => {
  await withDOM(async ({ render, window }) => {
    const { Dialog, useDialogPending } = await loadComponent(
      "src/components/ui/dialog.tsx",
    );
    let settle;
    function Form() {
      const [pending, setPending] = React.useState(true);
      settle = () => setPending(false);
      useDialogPending(pending);
      return React.createElement("input", {
        "aria-label": "Draft",
        defaultValue: "Keep this",
      });
    }
    function Fixture() {
      const [open, setOpen] = React.useState(true);
      return React.createElement(
        Dialog,
        { title: "Save record", open, onOpenChange: setOpen },
        React.createElement(Form),
      );
    }
    await render(React.createElement(Fixture));
    const dialog = document.querySelector('[role="dialog"]'),
      close = dialog.querySelector('button[aria-label="Close dialog"]');
    assert.ok(close.disabled);
    await React.act(async () => {
      close.click();
      document.dispatchEvent(
        new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      );
    });
    assert.ok(document.querySelector('[role="dialog"]') === dialog);
    assert.equal(dialog.dataset.state, "open");
    await React.act(async () => settle());
    assert.ok(!close.disabled);
    await React.act(async () => close.click());
    assert.ok(!document.querySelector('[role="dialog"]'));
  });
});
