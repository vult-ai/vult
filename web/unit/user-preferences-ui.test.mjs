import { phosphorPlugin } from "./helpers/phosphor-plugin.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { createRequire } from "node:module";
import { build } from "esbuild";
import { React, withDOM } from "./helpers/dom.mjs";

const bundle = await build({
  stdin: {
    contents:
      'export { UserPreferencesSettings } from "./components/workspaces/user-preferences"; export { UserPreferencesProvider,useUserPreferences } from "./lib/user-preferences-context"; export { createPreferenceStore,preferenceStorageKey,DEFAULT_USER_PREFERENCES } from "./lib/user-preferences"; export { RecordDate } from "./components/ui/record-date";',
    resolveDir: new URL("../src", import.meta.url).pathname,
    loader: "tsx",
  },
  jsx: "automatic",
  bundle: true,
  write: false,
  platform: "node",
  format: "cjs",
  packages: "external", plugins: [phosphorPlugin],
  loader: { ".module.css": "empty", ".css": "empty" },
});
const loaded = { exports: {} };
new Function("module", "exports", "require", bundle.outputFiles[0].text)(
  loaded,
  loaded.exports,
  createRequire(import.meta.url),
);
const {
  UserPreferencesProvider,
  UserPreferencesSettings,
  useUserPreferences,
  createPreferenceStore,
  preferenceStorageKey,
  DEFAULT_USER_PREFERENCES: defaults,
  RecordDate,
} = loaded.exports;
const memory = () => {
  const data = new Map();
  return {
    data,
    getItem: (key) => data.get(key) ?? null,
    setItem: (key, value) => data.set(key, value),
  };
};
const scope = { kind: "browser" };
const tree = (
  owner,
  storage,
  children = React.createElement(UserPreferencesSettings),
) =>
  React.createElement(
    UserPreferencesProvider,
    { scope: owner, storage },
    children,
  );
async function choose(label, value) {
  const select = document.querySelector(`select[aria-label="${label}"]`);
  assert.ok(select, label);
  await React.act(async () => {
    select.value = value;
    select.dispatchEvent(new window.Event("change", { bubbles: true }));
  });
}

test("inline canonical controls update previews and browser persistence without a request or modal", async () => {
  await withDOM(async ({ render }) => {
    const previous = globalThis.fetch;
    globalThis.fetch = () => {
      throw Error("Presentation preferences must not write accounting data.");
    };
    try {
      const storage = memory();
      await render(tree(scope, storage));
      assert.match(document.body.textContent, /English/);
      assert.match(
        document.querySelector('[role="status"]').textContent,
        /Saved in this browser/,
      );
      assert.equal(
        document.querySelector('select[aria-label="Language"]'),
        null,
      );
      assert.equal(
        document.querySelector('select[aria-label="Theme preference"]'),
        null,
        "Existing Appearance stays owned by the existing Settings host",
      );
      await choose("Date format", "day-month-year");
      await choose("Regional format", "de-DE");
      await choose("Time format", "12h");
      await choose("Negative numbers", "parentheses");
      assert.match(document.body.textContent, /27\/10\/2026/);
      assert.match(document.body.textContent, /8:30:00 PM UTC/);
      assert.match(document.body.textContent, /\(1\.234,56\s\$\)/);
      assert.equal(document.querySelector('[role="dialog"]'), null);
      assert.equal(document.querySelector('button[type="submit"]'), null);
      const saved = JSON.parse(storage.getItem(preferenceStorageKey(scope)));
      assert.equal(saved.preferences.regional_format, "de-DE");
      assert.equal(saved.preferences.date_format, "day-month-year");
      assert.equal(document.querySelector("time").dateTime, "2026-10-27");
      assert.equal(
        document.querySelector("time").textContent,
        "27/10/2026",
      );
    } finally {
      globalThis.fetch = previous;
    }
  });
});

test("switching verified User scope never renders or overwrites the previous user's choices", async () => {
  await withDOM(async ({ render }) => {
    const storage = memory();
    const a = { kind: "user", id: "synthetic-a" },
      b = { kind: "user", id: "synthetic-b" };
    createPreferenceStore(a, storage).update({
      regional_format: "de-DE",
      date_format: "iso",
    });
    const seen = [];
    function Observe() {
      const prefs = useUserPreferences();
      seen.push({ id: prefs.scope.id, format: prefs.value.regional_format });
      return React.createElement(UserPreferencesSettings);
    }
    const child = React.createElement(Observe);
    await render(tree(a, storage, child));
    assert.equal(
      document.querySelector('select[aria-label="Regional format"]').value,
      "de-DE",
    );
    await render(tree(b, storage, child));
    assert.equal(
      document.querySelector('select[aria-label="Regional format"]').value,
      "en-US",
    );
    assert.ok(
      seen
        .filter((item) => item.id === "synthetic-b")
        .every((item) => item.format === "en-US"),
    );
    await choose("Regional format", "pl-PL");
    await render(tree(a, storage, child));
    assert.equal(
      document.querySelector('select[aria-label="Regional format"]').value,
      "de-DE",
    );
    assert.equal(
      document.querySelector('select[aria-label="Date format"]').value,
      "iso",
    );
    assert.equal(
      createPreferenceStore(b, storage).getSnapshot().value.regional_format,
      "pl-PL",
    );
  });
});

test("disabled or unreadable storage never claims a successful save", async () => {
  await withDOM(async ({ render }) => {
    await render(tree(scope, null));
    await choose("Date format", "iso");
    assert.match(
      document.querySelector('[role="status"]').textContent,
      /could not be saved/,
    );
    assert.equal(
      document.querySelector('select[aria-label="Date format"]').value,
      "iso",
    );
    const storage = memory();
    storage.setItem(preferenceStorageKey(scope), "future data");
    await render(tree(scope, storage));
    await choose("Date format", "day-month-year");
    assert.match(
      document.querySelector('[role="status"]').textContent,
      /without replacing them/,
    );
    assert.equal(storage.getItem(preferenceStorageKey(scope)), "future data");
  });
});

test("cross-tab changes stay within the active owner and unsubscribe on scope change", async () => {
  await withDOM(async ({ render, window }) => {
    const a = { kind: "user", id: "synthetic-a" },
      b = { kind: "user", id: "synthetic-b" };
    await render(tree(a, window.localStorage));
    const store = createPreferenceStore(a, window.localStorage);
    store.update({ date_format: "iso" });
    await React.act(async () =>
      window.dispatchEvent(
        new window.StorageEvent("storage", {
          key: store.key,
          storageArea: window.localStorage,
        }),
      ),
    );
    assert.equal(
      document.querySelector('select[aria-label="Date format"]').value,
      "iso",
    );
    await render(tree(b, window.localStorage));
    await React.act(async () =>
      window.dispatchEvent(
        new window.StorageEvent("storage", {
          key: store.key,
          storageArea: window.localStorage,
        }),
      ),
    );
    assert.equal(
      document.querySelector('select[aria-label="Date format"]').value,
      "standard",
    );
  });
});
