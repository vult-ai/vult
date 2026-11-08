import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const { useReadRetry, Button, ErrorNotice, responseFailure, presentFailure } =
  await loadComponent("tests/fixtures/read-retry.ts");

test("read retry blocks bursts while pending and for one second after settlement", async context => {
  await withDOM(async ({ render }) => {
    let finish, attempts = 0;
    function Harness() {
      const action = useReadRetry(async () => {
        attempts++;
        await new Promise(resolve => { finish = resolve; });
      });
      return React.createElement(Button, {
        onClick: action.retry, busy: action.running, disabled: action.coolingDown,
      }, "Retry");
    }
    await render(React.createElement(Harness));
    context.mock.timers.enable({ apis: ["setTimeout"] });
    try {
      const button = document.querySelector("button");
      await React.act(async () => { for (let i = 0; i < 30; i++) button.click(); });
      assert.equal(attempts, 1);
      assert.equal(button.disabled, true);
      assert.equal(button.getAttribute("aria-busy"), "true");
      await React.act(async () => finish());
      assert.equal(button.disabled, true);
      assert.equal(button.getAttribute("aria-busy"), null);
      await React.act(async () => { for (let i = 0; i < 30; i++) button.click(); });
      assert.equal(attempts, 1);
      await React.act(async () => context.mock.timers.tick(999));
      assert.equal(button.disabled, true);
      await React.act(async () => context.mock.timers.tick(1));
      assert.equal(button.disabled, false);
      await React.act(async () => button.click());
      assert.equal(attempts, 2);
      await React.act(async () => finish());
    } finally { context.mock.timers.reset(); }
  });
});

test("the retry lock survives a loading screen that removes and restores its button", async context => {
  await withDOM(async ({ render }) => {
    let finish, attempts = 0;
    function Harness() {
      const [loading, setLoading] = React.useState(false);
      const action = useReadRetry(async () => {
        attempts++;
        setLoading(true);
        await new Promise(resolve => { finish = resolve; });
        setLoading(false);
      }, loading);
      return loading ? React.createElement("p", null, "Opening Book…") :
        React.createElement(Button, { onClick: action.retry, busy: action.running, disabled: action.coolingDown }, "Retry");
    }
    await render(React.createElement(Harness));
    context.mock.timers.enable({ apis: ["setTimeout"] });
    try {
      await React.act(async () => { for (let i = 0; i < 30; i++) document.querySelector("button")?.click(); });
      assert.equal(attempts, 1);
      assert.equal(document.querySelector("button"), null);
      await React.act(async () => finish());
      const button = document.querySelector("button");
      assert.equal(button.disabled, true);
      await React.act(async () => button.click());
      assert.equal(attempts, 1);
      await React.act(async () => context.mock.timers.tick(1000));
      assert.equal(button.disabled, false);
    } finally { context.mock.timers.reset(); }
  });
});

test("shared error retries also throttle immediate rejected promises", async context => {
  await withDOM(async ({ render }) => {
    let attempts = 0;
    await render(React.createElement(ErrorNotice, {
      error: responseFailure(503, {}), intent: "read", resource: "dashboard",
      onRetry: async () => { attempts++; throw new TypeError("Failed to fetch"); },
    }));
    context.mock.timers.enable({ apis: ["setTimeout"] });
    try {
      const button = document.querySelector("button");
      await React.act(async () => { for (let i = 0; i < 30; i++) button.click(); });
      assert.equal(attempts, 1);
      assert.equal(button.disabled, true);
      assert.match(document.querySelector(".error-notice").textContent, /couldn’t connect/);
      await React.act(async () => { for (let i = 0; i < 30; i++) button.click(); });
      assert.equal(attempts, 1);
      await React.act(async () => context.mock.timers.tick(1000));
      await React.act(async () => button.click());
      assert.equal(attempts, 2);
    } finally { context.mock.timers.reset(); }
  });
});

test("an unavailable feature offers no retry while transient errors remain retryable", async () => {
  await withDOM(async ({ render }) => {
    const unsupported = responseFailure(501, { code: "FEATURE_UNAVAILABLE" });
    await render(React.createElement(ErrorNotice, {
      error: unsupported, intent: "read", resource: "dashboard",
      onRetry: () => assert.fail("an unavailable feature must not retry"),
    }));
    assert.match(document.body.textContent, /Dashboard isn’t available yet\./);
    assert.equal(document.querySelector("button"), null);
    assert.equal(presentFailure(responseFailure(503, {})).recovery, "retry");
    assert.equal(presentFailure(responseFailure(501, { code: "FEATURE_UNAVAILABLE" }, "write")).outcome, "unknown");
  });
});
