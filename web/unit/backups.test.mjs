import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const wait = (ms = 30) => React.act(async () => new Promise((r) => setTimeout(r, ms)));
const text = () => document.body.textContent.replace(/\s+/g, " ");
const book = { id: "11111111-1111-4111-8111-111111111111", name: "Eightball Inc.", currency: "USD", scale: 2 };
const lost = "Everything recorded in Vult after November 13, 2026 at 9:00 AM will be lost, in every Book, and you'll sign in again.";
const backup = { id: "installer-20261113T090000Z-0a0b0c0d", created_at: "2026-11-13T09:00:00Z", version: "0.8.0-rc.5", to: "0.8.0", reason: "upgrade", size: 4194304, lost };

test("after an upgrade, one toast says the Vult was backed up first; it stays until closed or Restore… is used", async () =>
  withDOM(async ({ render }) => {
    const { UpgradeNotice } = await loadComponent("src/components/backups/upgrade-notice.tsx");
    const shown = [], dismissed = [];
    await render(React.createElement(UpgradeNotice, { book,
      read: async () => ({ available: true, backup, notice: { backup_id: backup.id, version: "0.8.0", backed_up_at: backup.created_at } }),
      dismiss: async (id) => { dismissed.push(id); },
      show: (id, message, action, onClose) => { shown.push({ id, message, label: action.label, onClose }); } }));
    await wait();
    assert.equal(shown.length, 1);
    assert.equal(shown[0].message, "Updated to 0.8.0. Your Vult was backed up first.");
    assert.equal(shown[0].label, "Restore…");
    assert.equal(document.body.textContent, "", "nothing in the page itself, so nothing moves");
    await wait(300);
    assert.deepEqual(dismissed, [], "not gone until the owner closes it");
    shown[0].onClose();
    assert.deepEqual(dismissed, [backup.id]);
  }));

test("no toast without an upgrade", async () =>
  withDOM(async ({ render }) => {
    const { UpgradeNotice } = await loadComponent("src/components/backups/upgrade-notice.tsx");
    const shown = [];
    await render(React.createElement(UpgradeNotice, { book, read: async () => ({ available: true, backup, notice: null }), show: (...a) => shown.push(a) }));
    await wait();
    assert.equal(shown.length, 0);
  }));

test("restoring says what's lost in the server's words, needs the typed word, keeps waiting through gaps, then says it's done", async () =>
  withDOM(async ({ render }) => {
    const { RestoreSettings } = await loadComponent("src/components/backups/restore-settings.tsx");
    const started = [];
    let polls = 0;
    await render(React.createElement(RestoreSettings, {
      active: true,
      read: async () => ({ available: true, backup, notice: null }),
      start: async (id) => { started.push(id); return { job_id: "0123456789abcdef0123456789abcdef" }; },
      poll: async () => {
        polls++;
        // Not picked up yet, then Vult stopped for a moment, then running, then done.
        if (polls === 1) throw Object.assign(new Error("not found"), { status: 404 });
        if (polls === 2) throw new TypeError("Failed to fetch");
        return polls < 4 ? { state: "running", step: "Putting the backup back" } : { state: "done" };
      },
    }));
    await wait();
    assert.match(text(), /Latest backup.*· Vult 0\.8\.0-rc\.5 · kept before updating to 0\.8\.0/);
    await React.act(async () => [...document.querySelectorAll("button")].find((b) => b.textContent === "Restore this backup").click());
    await wait();
    assert.match(text(), new RegExp(lost.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
    const restore = () => [...document.querySelectorAll("button")].find((b) => b.textContent.trim() === "Restore");
    assert.equal(restore().disabled, true);
    const box = document.querySelector('input[name="confirm"]');
    await React.act(async () => {
      Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set.call(box, "RESTORE");
      box.dispatchEvent(new window.Event("input", { bubbles: true }));
    });
    await React.act(async () => restore().click());
    await wait(5600);
    assert.deepEqual(started, [backup.id]);
    assert.match(text(), /Restored\. Your Vult is as it was on .*\.\s*Sign in again to continue\./);
  }));

test("a restore that fails says so in the server's words", async () =>
  withDOM(async ({ render }) => {
    const { RestoreSettings } = await loadComponent("src/components/backups/restore-settings.tsx");
    await render(React.createElement(RestoreSettings, {
      active: true,
      read: async () => ({ available: true, backup, notice: null }),
      start: async () => ({ job_id: "j" }),
      poll: async () => ({ state: "failed", error: "The backup couldn't be read, so Vult was left as it was." }),
    }));
    await wait();
    await React.act(async () => [...document.querySelectorAll("button")].find((b) => b.textContent === "Restore this backup").click());
    await wait();
    const box = document.querySelector('input[name="confirm"]');
    await React.act(async () => {
      Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set.call(box, "RESTORE");
      box.dispatchEvent(new window.Event("input", { bubbles: true }));
    });
    await React.act(async () => [...document.querySelectorAll("button")].find((b) => b.textContent.trim() === "Restore").click());
    await wait(100);
    assert.match(text(), /The backup couldn't be read, so Vult was left as it was\./);
  }));

test("arriving from the upgrade notice brings Restore into view once its backup is read", async () =>
  withDOM(async ({ render }) => {
    const { RestoreSettings, restoreSection } = await loadComponent("src/components/backups/restore-settings.tsx");
    const scrolled = [];
    window.HTMLElement.prototype.scrollIntoView = function () { scrolled.push(this.id); };
    window.history.replaceState(null, "", `/eightball/settings?tab=data#${restoreSection}`);
    await render(React.createElement(RestoreSettings, { active: true, read: async () => ({ available: true, backup, notice: null }) }));
    await wait();
    assert.deepEqual(scrolled, [restoreSection]);
    assert.ok(document.getElementById(restoreSection), "the section carries the address the notice links to");
  }));

test("Restore… waits for the notice to be marked seen before it leaves the page", async () =>
  withDOM(async ({ render }) => {
    const { UpgradeNotice } = await loadComponent("src/components/backups/upgrade-notice.tsx");
    const order = [];
    let finish;
    const shown = [];
    await render(React.createElement(UpgradeNotice, { book,
      read: async () => ({ available: true, backup, notice: { backup_id: backup.id, version: "0.8.0", backed_up_at: backup.created_at } }),
      dismiss: () => new Promise((r) => { finish = () => { order.push("seen"); r(); }; }),
      show: (id, message, action, onClose) => shown.push({ action, onClose }),
      open: (to) => order.push(`go ${to}`) }));
    await wait();
    // As the toast does it: close, then the action.
    shown[0].onClose();
    shown[0].action.onClick();
    await wait();
    assert.deepEqual(order, [], "doesn't leave while the notice is still being marked seen");
    finish();
    await wait();
    assert.equal(order[0], "seen");
    assert.match(order[1], /^go .*settings\?tab=data#restore$/);
  }));
