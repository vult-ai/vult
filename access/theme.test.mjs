import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { runInNewContext } from "node:vm";

const script = readFileSync(new URL("./theme.js", import.meta.url), "utf8");

function page({ stored = null, cookie = "", dark = false } = {}) {
  const classes = new Set();
  const listeners = {};
  const storage = new Map(stored === null ? [] : [["vult-theme", stored]]);
  const written = [];
  const attributes = (initial) => {
    const values = { ...initial };
    return { values, setAttribute: (k, v) => { values[k] = v; }, getAttribute: (k) => values[k] };
  };
  const button = { ...attributes({}), listeners: {}, addEventListener(type, fn) { this.listeners[type] = fn; }, focus() { document.activeElement = this; } };
  const items = ["light", "dark", "system"].map((id) => ({ ...attributes({}), dataset: { themeChoice: id }, listeners: {}, addEventListener(type, fn) { this.listeners[type] = fn; }, focus() { document.activeElement = this; } }));
  const menu = { hidden: true, listeners: {}, querySelectorAll: () => items, addEventListener(type, fn) { this.listeners[type] = fn; } };
  const control = { hidden: true, contains: (node) => node === button || items.includes(node) };
  const document = {
    readyState: "complete",
    activeElement: null,
    documentElement: { classList: { add: (c) => classes.add(c), remove: (...c) => c.forEach((x) => classes.delete(x)) }, dataset: {} },
    get cookie() { return cookie; },
    set cookie(value) { written.push(value); },
    getElementById: (id) => ({ theme: control, "theme-button": button, "theme-menu": menu })[id],
    addEventListener: (type, fn) => { listeners[type] = fn; },
  };
  runInNewContext(script, {
    document,
    location: { protocol: "https:" },
    localStorage: { getItem: (k) => storage.get(k) ?? null, setItem: (k, v) => storage.set(k, v) },
    window: { matchMedia: () => ({ matches: dark, addEventListener: () => {} }) },
  });
  return { classes, document, storage, written, button, items, menu, control, listeners };
}

test("the stored preference wins, then the shared cookie, then the system", () => {
  assert.deepEqual([...page({ stored: "light", cookie: "vult-theme=dark", dark: true }).classes], ["light-mode"]);
  assert.deepEqual([...page({ cookie: "other=1; vult-theme=dark" }).classes], ["dark-mode"]);
  const system = page({ dark: true });
  assert.deepEqual([...system.classes], ["dark-mode"]);
  assert.equal(system.document.documentElement.dataset.vultThemePreference, "system");
  assert.equal(system.control.hidden, false);
  assert.equal(system.button.values["aria-label"], "Theme: System");
});

test("choosing a theme applies it and saves it the way the app does", () => {
  const p = page();
  p.button.listeners.click();
  assert.equal(p.menu.hidden, false);
  assert.equal(p.document.activeElement, p.items[2]);
  p.items[1].listeners.click();
  assert.equal(p.menu.hidden, true);
  assert.deepEqual([...p.classes], ["dark-mode"]);
  assert.equal(p.storage.get("vult-theme"), "dark");
  assert.deepEqual(p.written, [
    "vult-theme=dark; Path=/; Max-Age=31536000; SameSite=Lax; Secure",
    "vult-theme-resolved=dark; Path=/; Max-Age=31536000; SameSite=Lax; Secure",
  ]);
  assert.equal(p.items[1].values["aria-checked"], "true");
  assert.equal(p.items[2].values["aria-checked"], "false");
  assert.equal(p.document.activeElement, p.button);
});

test("the menu closes with Escape and moves with the arrow keys", () => {
  const p = page({ stored: "light" });
  p.button.listeners.click();
  assert.equal(p.document.activeElement, p.items[0]);
  p.menu.listeners.keydown({ key: "ArrowUp", preventDefault() {} });
  assert.equal(p.document.activeElement, p.items[2]);
  p.menu.listeners.keydown({ key: "Escape" });
  assert.equal(p.menu.hidden, true);
  assert.equal(p.document.activeElement, p.button);
});
