import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

test("native event callers keep their target and local controls never enter pending", async () => {
  await withDOM(async ({ render }) => {
    const { Button } = await loadComponent("src/components/ui/button.tsx");
    const targets = [];
    await render(
      React.createElement(
        Button,
        { onClick: (event) => targets.push(event.currentTarget) },
        "Open details",
      ),
    );
    const button = document.querySelector("button");
    await React.act(async () => {
      button.click();
      button.click();
    });
    assert.deepEqual(targets, [button]);
    assert.equal(button.disabled, false);
    assert.equal(button.hasAttribute("data-loading"), false);
    await React.act(async () => button.click());
    assert.equal(targets.length, 2);
  });
});

test("an async action deduplicates presses and retains its accessible name until settled", async () => {
  await withDOM(async ({ render }) => {
    const { Button } = await loadComponent("src/components/ui/button.tsx");
    let done,
      calls = 0;
    await render(
      React.createElement(
        Button,
        {
          onClick: () => {
            calls++;
            return new Promise((resolve) => {
              done = resolve;
            });
          },
        },
        "Save entry",
      ),
    );
    const button = document.querySelector("button");
    await React.act(async () => {
      button.click();
      button.click();
    });
    assert.equal(calls, 1);
    assert.equal(button.disabled, true);
    assert.equal(button.getAttribute("aria-label"), "Save entry");
    assert.equal(button.getAttribute("aria-busy"), "true");
    await React.act(async () => done());
    assert.equal(document.querySelector("button"), button);
    assert.equal(button.disabled, false);
  });
});
