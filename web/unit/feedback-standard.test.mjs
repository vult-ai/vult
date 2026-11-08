import assert from "node:assert/strict";
import test from "node:test";
import { readdir, readFile } from "node:fs/promises";
import path from "node:path";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const { checkSource } = await import("../scripts/check-feedback.mjs");

async function sources(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  const nested = await Promise.all(
    entries.map((entry) =>
      entry.isDirectory()
        ? sources(path.join(dir, entry.name))
        : /\.[jt]sx?$/.test(entry.name)
          ? [path.join(dir, entry.name)]
          : [],
    ),
  );
  return nested.flat();
}

test("the interface keeps one standard for notices, confirmations and pending buttons", async () => {
  const failures = [];
  for (const file of await sources("src"))
    failures.push(
      ...checkSource(path.relative("src", file), await readFile(file, "utf8")).map(
        (message) => `${file}: ${message}`,
      ),
    );
  assert.deepEqual(failures, []);
});

test("the guard rejects each pattern the shared components replace", () => {
  const cases = [
    ['{saved && <p role="status">Saved.</p>}', /Use Notice for an outcome/],
    ['<div className="save-notice">Saved.</div>', /hand-built notice/],
    ['<Button variant="destructive">Delete</Button>', /ConfirmAction/],
    ['<Button busy={busy}>{busy ? "Saving…" : "Save"}</Button>', /Keep a button’s label/],
    ["<Notice>Saved to your Book</Notice>", /full stop/],
    ["<Notice>Saved!</Notice>", /full stop/],
  ];
  for (const [source, rule] of cases)
    assert.match(checkSource("components/example.tsx", source).join("\n"), rule, source);
  assert.deepEqual(
    checkSource(
      "components/example.tsx",
      '<p role="status">{count} files</p><Notice tone="info">Latest details loaded.</Notice><Button busy={busy}>Save</Button>',
    ),
    [],
  );
});

test("a notice speaks once through the shared live region, and cautions stay quiet", () =>
  withDOM(async ({ render, window }) => {
    const { Notice } = await loadComponent("src/components/ui/notice.tsx");
    await render(
      React.createElement(
        "div",
        null,
        React.createElement(Notice, null, "Saved to your Book."),
        React.createElement(Notice, { tone: "caution" }, "This period is closed."),
      ),
    );
    await React.act(() => new Promise((done) => window.setTimeout(done, 120)));
    const regions = document.querySelectorAll("[data-vult-announcer]");
    assert.equal(regions.length, 1);
    assert.equal(regions[0].getAttribute("aria-live"), "polite");
    assert.equal(regions[0].textContent, "Saved to your Book.");
    assert.equal(document.querySelectorAll('.vult-notice [role="status"], .vult-notice[role]').length, 0);
    assert.equal(document.querySelector('.vult-notice[data-tone="caution"]').textContent, "This period is closed.");
  }));

test("a confirmation keeps its label while pending and cannot be dismissed mid-step", () =>
  withDOM(async ({ render }) => {
    const { ConfirmAction } = await loadComponent("src/components/ui/confirm-action.tsx");
    let cancelled = 0;
    const props = {
      open: true,
      title: "Delete this file?",
      detail: "Example.pdf will be permanently deleted.",
      irreversible: true,
      confirmLabel: "Delete file",
      onConfirm() {},
      onCancel() {
        cancelled++;
      },
    };
    await render(React.createElement(ConfirmAction, props));
    const dialog = document.querySelector('[role="dialog"]');
    assert.match(dialog.textContent, /permanently deleted\. You can’t undo this\./);
    const buttons = [...dialog.querySelectorAll(".vult-confirm-actions button")];
    assert.deepEqual(buttons.map((button) => button.textContent.trim()), ["Cancel", "Delete file"]);
    await render(React.createElement(ConfirmAction, { ...props, busy: true }));
    const pending = [...document.querySelectorAll(".vult-confirm-actions button")];
    assert.ok(pending.every((button) => button.disabled));
    assert.equal(pending[1].getAttribute("aria-busy"), "true");
    assert.equal(document.querySelector(".dialog-close").disabled, true);
    assert.equal(cancelled, 0);
  }));
