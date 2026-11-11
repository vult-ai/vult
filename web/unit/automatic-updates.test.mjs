import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const { UpdateNotice, useUpdateNotice } = await loadComponent("src/components/shell/update-notice.tsx");
const { UpdatingOverlay, fetchJSON, setConnectionTiming } = await loadComponent("unit/fixtures/updating-overlay.ts");
const wait = (ms) => React.act(() => new Promise((resolve) => setTimeout(resolve, ms)));

const settle = () => React.act(async () => { await Promise.resolve(); await Promise.resolve(); });
const installer = { enabled: true, state: "idle", installed: { version: "0.7.0", schema: "0008" } };
const json = (value) => ({ ok: true, status: 200, json: async () => value, headers: new Headers() });

function Notice() {
  return React.createElement(UpdateNotice, useUpdateNotice(true));
}

test("with an installer, a newer release gets an Update button that asks for it", async () => {
  await withDOM(async ({ render, unmount }) => {
    const before = globalThis.fetch;
    const asked = [];
    let current = installer;
    globalThis.fetch = async (url, init = {}) => {
      asked.push({ url, method: init.method ?? "GET", body: init.body });
      if (url.endsWith("/update/install")) {
        current = { ...installer, requested: "0.7.1" };
        return json({ schema_version: 1, installer: current });
      }
      return json({ schema_version: 1, current: "0.7.0", latest: "0.7.1", available: true, installer: current });
    };
    try {
      await render(React.createElement(Notice));
      await settle();
      const notice = document.querySelector('[aria-label="Update available"]');
      assert.match(notice.textContent, /Vult 0\.7\.1 is available/);
      assert.equal(notice.querySelector("p"), null, "no subtitle");
      assert.equal(notice.querySelector("code"), null, "no Terminal command");
      const button = [...notice.querySelectorAll("button")].find((b) => b.textContent === "Update");
      await React.act(async () => button.click());
      await settle();
      const request = asked.find((a) => a.url.endsWith("/update/install"));
      assert.ok(request, "pressing Update asks the installer");
      assert.deepEqual(JSON.parse(request.body), { version: "0.7.1" });
      const after = document.querySelector('[aria-label="Update available"]');
      assert.ok(after.querySelector('[aria-busy="true"], [data-busy="true"], .vult-button[aria-disabled="true"], button[disabled]'), "the button shows it's working");
      const close = after.querySelector('[aria-label="Hide until the next release"]');
      assert.ok(close, "kept in place so the card doesn't shift");
      assert.equal(close.disabled, true, "can't be hidden while under way");
      assert.ok(close.hasAttribute("data-hidden"));
    } finally {
      await unmount();
      globalThis.fetch = before;
    }
  });
});

test("an update that didn't finish says nothing changed and offers to try again", async () => {
  await withDOM(async ({ render, unmount }) => {
    const before = globalThis.fetch;
    globalThis.fetch = async () => json({ schema_version: 1, current: "0.7.0", latest: "0.7.1", available: true, installer: { ...installer, last_outcome: "restored" } });
    try {
      await render(React.createElement(Notice));
      await settle();
      const notice = document.querySelector('[aria-label="Update available"]');
      assert.match(notice.textContent, /didn’t finish\. Nothing changed/);
      assert.ok([...notice.querySelectorAll("button")].some((b) => b.textContent === "Update"));
    } finally {
      await unmount();
      globalThis.fetch = before;
    }
  });
});

test("without an installer the card keeps the update command", async () => {
  await withDOM(async ({ render, unmount }) => {
    const before = globalThis.fetch;
    globalThis.fetch = async () => json({ schema_version: 1, current: "0.7.0", latest: "0.7.1", available: true });
    try {
      await render(React.createElement(Notice));
      await settle();
      const notice = document.querySelector('[aria-label="Update available"]');
      assert.equal(notice.querySelector("code").textContent, "vult update");
      assert.equal([...notice.querySelectorAll("button")].some((b) => b.textContent === "Update"), false);
    } finally {
      await unmount();
      globalThis.fetch = before;
    }
  });
});

test("while Vult is away one message covers the page, reads carry on, and it clears by itself", async () => {
  await withDOM(async ({ render, unmount }) => {
    const before = globalThis.fetch;
    setConnectionTiming({ probeEvery: 20, waitAtMost: 5000 });
    let up = false;
    const asked = [];
    globalThis.fetch = async (url, init = {}) => {
      asked.push([url, (init.method ?? "GET").toUpperCase()]);
      if (!up) throw new TypeError("Failed to fetch");
      return json(url === "/auth/session" ? { mode: "local" } : { value: 1 });
    };
    try {
      await render(React.createElement(UpdatingOverlay, { showAfter: 50, local: true }));
      let read;
      await React.act(async () => { read = fetchJSON("/api/v1/state"); });
      let write;
      await React.act(async () => {
        write = fetchJSON("/api/v1/entries", { method: "POST", body: "{}" }).then(() => "saved", (error) => error);
      });
      assert.equal(document.querySelector(".updating-overlay"), null, "a blip doesn't flash a message");
      await wait(80);
      const overlay = document.querySelector(".updating-overlay");
      assert.ok(overlay, "the message shows");
      assert.match(overlay.textContent, /Updating Vult/);
      assert.match(overlay.textContent, /Back in a moment/);
      assert.equal(document.getElementById("root").hasAttribute("inert"), true, "nothing behind it can be used");
      // Vult comes back.
      up = true;
      await wait(100);
      assert.deepEqual(await read, { value: 1 }, "the read carried on");
      const failure = await write;
      assert.notEqual(failure, "saved");
      assert.match(failure.message, /may already be saved/, "a write is never repeated behind the person's back");
      assert.equal(asked.filter(([url, method]) => url === "/api/v1/entries" && method === "POST").length, 1);
      assert.equal(document.querySelector(".updating-overlay"), null, "the message clears by itself");
      assert.equal(document.getElementById("root").hasAttribute("inert"), false);
    } finally {
      await unmount();
      globalThis.fetch = before;
    }
  });
});

test("a lost connection elsewhere fails as before, with no update message", async () => {
  await withDOM(async ({ render, unmount }) => {
    const before = globalThis.fetch;
    globalThis.fetch = async () => { throw new TypeError("Failed to fetch"); };
    try {
      await render(React.createElement(UpdatingOverlay, { showAfter: 10, local: false }));
      let failed;
      await React.act(async () => { failed = await fetchJSON("/api/v1/state").then(() => null, (error) => error); });
      assert.ok(failed, "the read fails straight away");
      await wait(50);
      assert.equal(document.querySelector(".updating-overlay"), null);
    } finally {
      await unmount();
      globalThis.fetch = before;
    }
  });
});

test("if Vult stays away, the message says so plainly", async () => {
  await withDOM(async ({ render, unmount }) => {
    const before = globalThis.fetch;
    setConnectionTiming({ probeEvery: 20, waitAtMost: 400 });
    globalThis.fetch = async () => { throw new TypeError("Failed to fetch"); };
    try {
      await render(React.createElement(UpdatingOverlay, { showAfter: 20, longAfter: 150, local: true }));
      await React.act(async () => { fetchJSON("/api/v1/state").catch(() => undefined); });
      await wait(200);
      const overlay = document.querySelector(".updating-overlay");
      assert.match(overlay.textContent, /Taking longer than usual/);
      assert.match(overlay.textContent, /restart your Mac/);
      await wait(400);
      assert.ok(document.querySelector(".updating-overlay"), "the message stays while Vult is away, however long");
      // Let the search end so the test can finish.
      globalThis.fetch = async () => json({ mode: "local" });
      await wait(100);
      assert.equal(document.querySelector(".updating-overlay"), null);
    } finally {
      await unmount();
      globalThis.fetch = before;
    }
  });
});
