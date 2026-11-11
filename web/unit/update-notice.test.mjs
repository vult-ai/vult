import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const { UpdateNotice, useUpdateNotice } = await loadComponent("src/components/shell/update-notice.tsx");
const command = "vult update";
const every = 12 * 60 * 60 * 1000;
const current = { current: "0.5.1", latest: "0.5.1", available: false };
const release = (version) => ({ current: "0.5.1", latest: version, available: true });
const response = (data) => ({ ok: true, status: 200, json: async () => data });
const settle = () => React.act(async () => { await Promise.resolve(); });

function Fixture({ local = true, copies = 1 }) {
  const notice = useUpdateNotice(local);
  return React.createElement(React.Fragment, null, ...Array.from({ length: copies }, (_, key) => React.createElement(UpdateNotice, { key, ...notice })));
}

function serve(status) {
  const asked = [];
  globalThis.fetch = async (url, options) => {
    assert.equal(url, "/api/v1/update", "reuse the session already read by the app");
    asked.push({ url, signal: options.signal });
    return response(typeof status === "function" ? status() : status);
  };
  return asked;
}

test("the notice copies the installed command and both sidebar copies share dismissal", async () => {
  await withDOM(async ({ render, unmount }) => {
    const before = globalThis.fetch;
    const asked = serve(release("0.5.2"));
    let copied;
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async (text) => { copied = text; } } });
    try {
      await render(React.createElement(Fixture, { copies: 2 }));
      await settle();
      const notices = document.querySelectorAll('[aria-label="Update available"]');
      assert.equal(notices.length, 2);
      assert.equal(asked.length, 1, "the rail and drawer share one check");
      assert.match(notices[0].textContent, /Vult 0\.5\.2 is available/);
      assert.equal(notices[0].querySelector("code").textContent, command);
      assert.equal(notices[0].querySelector("a").getAttribute("href"), "https://github.com/vult-ai/vult/releases/tag/v0.5.2");
      await React.act(async () => notices[0].querySelector('[aria-label="Copy command"]').click());
      assert.equal(copied, command);
      assert.ok(notices[0].querySelector('[aria-label="Copied"]'));
      await React.act(async () => notices[1].querySelector('[aria-label="Hide until the next release"]').click());
      assert.equal(document.querySelector('[aria-label="Update available"]'), null);
      await render(React.createElement(Fixture, { key: "after-reload" }));
      await settle();
      assert.equal(document.querySelector('[aria-label="Update available"]'), null, "a hidden release stays hidden");
    } finally {
      await unmount();
      globalThis.fetch = before;
    }
  });
});

test("an open app checks every twelve hours and a new release survives the previous dismissal", async (context) => {
  await withDOM(async ({ render, unmount }) => {
    const before = globalThis.fetch;
    let status = current;
    const asked = serve(() => status);
    context.mock.timers.enable({ apis: ["setTimeout", "Date"], now: new Date("2026-01-01T12:00:00Z") });
    try {
      await render(React.createElement(Fixture));
      await settle();
      assert.equal(asked.length, 1);
      status = release("0.5.2");
      window.dispatchEvent(new Event("focus"));
      document.dispatchEvent(new Event("visibilitychange"));
      await React.act(async () => context.mock.timers.tick(every - 1));
      assert.equal(asked.length, 1, "revisiting before the interval does not check again");
      await React.act(async () => context.mock.timers.tick(1));
      await settle();
      assert.equal(asked.length, 2);
      assert.match(document.querySelector('[aria-label="Update available"]').textContent, /0\.5\.2/);
      await React.act(async () => document.querySelector('[aria-label="Hide until the next release"]').click());
      await React.act(async () => context.mock.timers.tick(every));
      await settle();
      assert.equal(document.querySelector('[aria-label="Update available"]'), null, "the same release stays hidden after refresh");
      status = release("0.5.3");
      await React.act(async () => context.mock.timers.tick(every));
      await settle();
      assert.match(document.querySelector('[aria-label="Update available"]').textContent, /0\.5\.3/);
    } finally {
      await unmount();
      context.mock.timers.reset();
      globalThis.fetch = before;
    }
  });
});

test("a hidden app waits until it becomes visible and focus cannot duplicate an in-flight check", async (context) => {
  await withDOM(async ({ render, unmount }) => {
    const before = globalThis.fetch;
    let visibility = "hidden";
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => visibility });
    const asked = [];
    let finish;
    globalThis.fetch = async (url) => {
      asked.push(url);
      return new Promise((resolve) => { finish = () => resolve(response(release("0.5.2"))); });
    };
    context.mock.timers.enable({ apis: ["setTimeout", "Date"] });
    try {
      await render(React.createElement(Fixture));
      await React.act(async () => context.mock.timers.tick(every));
      assert.equal(asked.length, 0);
      visibility = "visible";
      await React.act(async () => document.dispatchEvent(new Event("visibilitychange")));
      window.dispatchEvent(new Event("focus"));
      document.dispatchEvent(new Event("visibilitychange"));
      assert.equal(asked.length, 1);
      await React.act(async () => finish());
      assert.ok(document.querySelector('[aria-label="Update available"]'));
      visibility = "hidden";
      await React.act(async () => context.mock.timers.tick(every));
      assert.equal(asked.length, 1);
      visibility = "visible";
      await React.act(async () => window.dispatchEvent(new Event("focus")));
      assert.equal(asked.length, 2, "returning after the interval checks again");
      await React.act(async () => finish());
    } finally {
      await unmount();
      context.mock.timers.reset();
      globalThis.fetch = before;
    }
  });
});

test("session loss and unmount abort requests and remove the refresh timer and listeners", async (context) => {
  await withDOM(async ({ render, unmount }) => {
    const before = globalThis.fetch;
    const asked = [];
    let finish;
    globalThis.fetch = async (url, options) => {
      asked.push({ url, signal: options.signal });
      return new Promise((resolve) => { finish = () => resolve(response(release("0.5.2"))); });
    };
    context.mock.timers.enable({ apis: ["setTimeout", "Date"] });
    try {
      await render(React.createElement(Fixture));
      assert.equal(asked.length, 1);
      await render(React.createElement(Fixture, { local: false }));
      assert.ok(asked[0].signal.aborted);
      await React.act(async () => finish());
      assert.equal(document.querySelector('[aria-label="Update available"]'), null, "a late response cannot revive the notice");
      await render(React.createElement(Fixture));
      assert.equal(asked.length, 2);
      await unmount();
      assert.ok(asked[1].signal.aborted);
      await React.act(async () => finish());
      await React.act(async () => context.mock.timers.tick(every));
      window.dispatchEvent(new Event("focus"));
      document.dispatchEvent(new Event("visibilitychange"));
      assert.equal(asked.length, 2);
    } finally {
      await unmount();
      context.mock.timers.reset();
      globalThis.fetch = before;
    }
  });
});

test("nothing shows when Vult is current or the verified session is not local", async () => {
  for (const local of [true, false]) {
    await withDOM(async ({ render, unmount }) => {
      const before = globalThis.fetch;
      const asked = serve(local ? current : release("0.5.2"));
      try {
        await render(React.createElement(Fixture, { local }));
        await settle();
        assert.equal(document.querySelector('[aria-label="Update available"]'), null);
        assert.equal(asked.length, local ? 1 : 0);
      } finally {
        await unmount();
        globalThis.fetch = before;
      }
    });
  }
});

test("helper discovery is followed promptly without starting an update", async (context) => {
  await withDOM(async ({ render, unmount }) => {
    const before = globalThis.fetch;
    let status = { ...current, installer: { enabled: true, state: "idle", checking: true } };
    const asked = serve(() => status);
    context.mock.timers.enable({ apis: ["setTimeout", "Date"] });
    try {
      await render(React.createElement(Fixture));
      await settle();
      assert.equal(asked.length, 1);
      assert.equal(document.querySelector('[aria-label="Update available"]'), null);
      status = { ...release("0.7.1"), installer: { enabled: true, state: "idle" } };
      await React.act(async () => context.mock.timers.tick(3000));
      await settle();
      assert.equal(asked.length, 2, "discovering a release does not wait twelve hours");
      const notice = document.querySelector('[aria-label="Update available"]');
      assert.match(notice.textContent, /0\.7\.1/);
      assert.ok([...notice.querySelectorAll("button")].some((button) => button.textContent === "Update" && !button.disabled));
      await React.act(async () => context.mock.timers.tick(3000));
      assert.equal(asked.length, 2, "the fast polling ends when discovery finishes");
    } finally {
      await unmount();
      context.mock.timers.reset();
      globalThis.fetch = before;
    }
  });
});
