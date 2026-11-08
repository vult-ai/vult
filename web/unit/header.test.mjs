import { phosphorPlugin } from "./helpers/phosphor-plugin.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { build } from "esbuild";
import { createRequire } from "node:module";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const key = (window, target, value, options = {}) =>
  target.dispatchEvent(
    new window.KeyboardEvent("keydown", {
      key: value,
      bubbles: true,
      cancelable: true,
      ...options,
    }),
  );

const finderBook = { id: "11111111-1111-4111-8111-111111111111", name: "Example Company", currency: "USD", scale: 2 };
const finderProps = (calls = []) => ({
  book: finderBook,
  pages: [{ id: "dashboard", label: "Dashboard", icon: () => null }],
  onOpenEntry: (id) => calls.push(["entry", id]),
  onOpenPage: (id) => calls.push(["page", id]),
  onShowTransactions: (q) => calls.push(["transactions", q]),
  onOpenAccount: (id) => calls.push(["account", id]),
  onShowSources: (q) => calls.push(["sources", q]),
});
const settle = () => React.act(async () => new Promise((done) => setTimeout(done, 50)));

test("⌘K opens the finder, keeps the last search selected, and returns focus to Search", async () => {
  await withDOM(async ({ render, window }) => {
    const { BookFinder } = await loadComponent("src/components/shell/book-finder.tsx");
    const savedFetch = globalThis.fetch;
    globalThis.fetch = async () => ({ ok: true, json: async () => ({ query: "", transactions: { items: [], total: 0 }, accounts: { items: [], total: 0 }, counterparties: { items: [], total: 0 }, sources: { items: [], total: 0 } }) });
    try {
      await render(React.createElement(BookFinder, finderProps()));
      const trigger = document.querySelector('button[aria-label="Search this Book"]');
      await React.act(async () => key(window, document.body, "k", { ctrlKey: true }));
      await settle();
      const field = () => document.querySelector('[role="dialog"] input');
      assert.ok(field(), "Ctrl+K opens the finder outside a Mac");
      assert.equal(trigger.getAttribute("aria-expanded"), "true");
      assert.equal(document.activeElement, field(), "the field has focus on opening");
      assert.equal(trigger.getAttribute("aria-keyshortcuts"), "Control+K");
      await React.act(async () => {
        const set = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set;
        set.call(field(), "amazon");
        field().dispatchEvent(new window.Event("input", { bubbles: true }));
      });
      assert.equal(field().selectionEnd - field().selectionStart, 0, "text typed since opening is never selected");
      await React.act(async () => key(window, document.body, "k", { ctrlKey: true }));
      await settle();
      assert.equal(field(), null, "the shortcut toggles the finder closed");
      assert.equal(document.activeElement, trigger, "focus returns to Search");
      await React.act(async () => key(window, document.body, "k", { ctrlKey: true }));
      await settle();
      assert.equal(field().value, "amazon", "the last search is kept");
      assert.equal(field().selectionEnd - field().selectionStart, 6, "and selected, so typing replaces it");
    } finally {
      globalThis.fetch = savedFetch;
    }
  });
});

test("outside a Mac, ⌘K is not the finder's shortcut", async () => {
  await withDOM(async ({ render, window }) => {
    const { BookFinder } = await loadComponent("src/components/shell/book-finder.tsx");
    await render(React.createElement(BookFinder, finderProps()));
    await React.act(async () => key(window, document.body, "k", { metaKey: true }));
    await settle();
    assert.equal(document.querySelector('[role="dialog"] input'), null);
  });
});

test("the finder yields its shortcut to an open dialog", async () => {
  await withDOM(async ({ render, window }) => {
    const { BookFinder } = await loadComponent("src/components/shell/book-finder.tsx");
    await render(
      React.createElement(
        "div",
        null,
        React.createElement(BookFinder, finderProps()),
        React.createElement("section", { role: "dialog", "data-state": "open", "aria-label": "Entry" }, React.createElement("button", null, "Save")),
      ),
    );
    await React.act(async () => key(window, document.querySelector("section button"), "k", { ctrlKey: true }));
    await settle();
    assert.equal(document.querySelector('button[aria-label="Search this Book"]').getAttribute("aria-expanded"), "false");
  });
});

test("the profile menu preserves Appearance and opens personal settings without replacing the workspace", async () => {
  await withDOM(async ({ render, window }) => {
    const bundle = await build({
      stdin: {
        contents:
          'export {ProfileMenu} from "./src/components/shell/profile-menu"; export {Theme} from "./src/design-system/theme";',
        resolveDir: process.cwd(),
        loader: "tsx",
      },
      bundle: true,
      write: false,
      platform: "node",
      format: "cjs",
      packages: "external", plugins: [phosphorPlugin],
      loader: { ".css": "empty", ".module.css": "empty" },
    });
    const module = { exports: {} };
    new Function("module", "exports", "require", bundle.outputFiles[0].text)(
      module,
      module.exports,
      createRequire(import.meta.url),
    );
    const { ProfileMenu, Theme } = module.exports;
    await render(
      React.createElement(
        Theme,
        { initialTheme: "light", initialPreference: "light" },
        React.createElement(ProfileMenu),
        React.createElement(
          "main",
          null,
          React.createElement("input", {
            "aria-label": "Draft",
            defaultValue: "Keep this",
          }),
        ),
      ),
    );
    const trigger = document.querySelector(
      'button[aria-label="Open profile menu"]',
    );
    const main = document.querySelector("main");
    const draft = main.querySelector("input");
    assert.ok(trigger.className.includes("size-8"));
    assert.equal(
      document.querySelector('[role="group"][aria-label="Theme"]'),
      null,
    );
    await React.act(async () => trigger.click());
    const appearance = [...document.querySelectorAll('[role="menuitem"]')].find(
      (item) => item.textContent.includes("Appearance"),
    );
    assert.ok(appearance);
    await React.act(async () => {
      appearance.focus();
      key(window, appearance, "ArrowRight");
    });
    const menu = document.querySelector(
      '[role="menu"][aria-label="Appearance"]',
    );
    assert.ok(menu);
    const items = [...menu.querySelectorAll('[role="menuitemradio"]')];
    assert.deepEqual(
      items.map((item) => item.textContent),
      ["System", "Light", "Dark"],
    );
    await React.act(async () => items[2].click());
    assert.equal(document.documentElement.dataset.vultThemePreference, "dark");
    assert.equal(document.querySelector("main"), main);
    assert.equal(draft.value, "Keep this");
    await React.act(async () => trigger.click());
    await React.act(async () =>
      [...document.querySelectorAll('[role="menuitem"]')]
        .find((item) => item.textContent === "Profile & preferences")
        .click(),
    );
    const dialog = document.querySelector('[role="dialog"][data-state="open"]');
    assert.ok(dialog);
    assert.match(dialog.textContent, /Your settings/);
    assert.equal(document.querySelector("main"), main);
    assert.equal(draft.value, "Keep this");
    assert.equal(
      dialog.querySelector('input[aria-label="Display name"]').value,
      "",
    );
  });
});
