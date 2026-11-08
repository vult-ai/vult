import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent, withDOM } from "./helpers/dom.mjs";

function collection() {
  const owner = document.createElement("div");
  owner.className = "collection-inspector";
  owner.innerHTML =
    '<div data-outer><table><tbody data-vult-view-scroll="fixture:body"><tr data-record-id="same-row"><td><button>Open row</button></td></tr></tbody></table></div>';
  document.body.append(owner);
  return {
    owner,
    outer: owner.querySelector("[data-outer]"),
    body: owner.querySelector("tbody"),
    trigger: owner.querySelector("button"),
  };
}

const frame = (window) =>
  new Promise((resolve) => window.requestAnimationFrame(resolve));

for (const compact of [false, true]) {
  test(`${compact ? "compact" : "docked"} restoration wins nested focus scrolling and preserves enclosing scroll positions`, async () => {
    await withDOM(async ({ window }) => {
      const { captureInspectorScroll, restoreInspectorScroll } =
        await loadComponent("src/design-system/collection-inspector.tsx");
      const { owner, outer, body, trigger } = collection();
      if (compact) owner.setAttribute("data-compact", "");
      body.scrollTop = 140;
      body.scrollLeft = 18;
      outer.scrollTop = 250;
      const saved = captureInspectorScroll(trigger);
      trigger.addEventListener("focus", () =>
        queueMicrotask(() =>
          queueMicrotask(() => {
            body.scrollTop = 999;
            body.scrollLeft = 77;
            outer.scrollTop = 800;
            body.dispatchEvent(new window.Event("scroll"));
          }),
        ),
      );
      trigger.focus({ preventScroll: true });
      restoreInspectorScroll(saved);
      await frame(window);
      assert.equal(body.scrollTop, 140);
      assert.equal(body.scrollLeft, 18);
      assert.equal(outer.scrollTop, 250);
      assert.equal(document.activeElement, trigger);
    });
  });
}

test("pending restoration yields to a new user scroll and can be canceled on reopening or unmount", async () => {
  await withDOM(async ({ window }) => {
    const { captureInspectorScroll, restoreInspectorScroll } =
      await loadComponent("src/design-system/collection-inspector.tsx");
    const { body, trigger } = collection();
    body.scrollTop = 140;
    const saved = captureInspectorScroll(trigger);
    const cancel = restoreInspectorScroll(saved);
    assert.equal(typeof cancel, "function");
    body.dispatchEvent(new window.WheelEvent("wheel", { bubbles: true }));
    body.scrollTop = 300;
    await frame(window);
    assert.equal(
      body.scrollTop,
      300,
      "a user's next scroll must not be pulled back",
    );
    const cancelReopen = restoreInspectorScroll(saved);
    cancelReopen();
    body.scrollTop = 420;
    await frame(window);
    assert.equal(
      body.scrollTop,
      420,
      "old close callbacks must not affect the next inspection",
    );
  });
});

test("a newer bookmark replaces pending restoration and the guard releases after focus settles", async () => {
  await withDOM(async ({ window }) => {
    const { captureInspectorScroll, restoreInspectorScroll } =
      await loadComponent("src/design-system/collection-inspector.tsx");
    const { body, trigger } = collection();
    body.scrollTop = 140;
    restoreInspectorScroll(captureInspectorScroll(trigger));
    body.scrollTop = 280;
    restoreInspectorScroll(captureInspectorScroll(trigger));
    body.scrollTop = 999;
    body.dispatchEvent(new window.Event("scroll"));
    await frame(window);
    assert.equal(body.scrollTop, 280);
    await frame(window);
    await frame(window);
    body.scrollTop = 420;
    body.dispatchEvent(new window.Event("scroll"));
    assert.equal(
      body.scrollTop,
      420,
      "restoration must not become a scroll lock",
    );
  });
});
