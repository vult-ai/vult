import { phosphorPlugin } from "./helpers/phosphor-plugin.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { build } from "esbuild";
import { createRequire } from "node:module";
import { React, withDOM } from "./helpers/dom.mjs";
const photo = "data:image/jpeg;base64,/9j/2Q==";
const memory = () => {
  const data = new Map();
  return {
    data,
    getItem: (k) => data.get(k) ?? null,
    setItem: (k, v) => data.set(k, v),
  };
};
async function components() {
  const bundle = await build({
    stdin: {
      contents:
        'export { ProfileMenu } from "./components/shell/profile-menu"; export { Theme } from "./design-system/theme"; export { createProfileStore } from "./lib/user-profile";',
      resolveDir: new URL("../src", import.meta.url).pathname,
      loader: "tsx",
    },
    jsx: "automatic",
    bundle: true,
    write: false,
    platform: "node",
    format: "cjs",
    packages: "external", plugins: [phosphorPlugin],
    loader: { ".css": "empty", ".module.css": "empty" },
  });
  const m = { exports: {} };
  new Function("module", "exports", "require", bundle.outputFiles[0].text)(
    m,
    m.exports,
    createRequire(import.meta.url),
  );
  return m.exports;
}
const click = async (node) => {
  assert.ok(node);
  await React.act(async () => {
    node.click();
    await new Promise(requestAnimationFrame);
  });
};
const find = (selector, text) =>
  [...document.querySelectorAll(selector)].find(
    (n) => n.textContent.trim() === text,
  );
async function open() {
  await click(document.querySelector('button[aria-label="Open profile menu"]'));
  await click(find('[role="menuitem"]', "Profile & preferences"));
  return document.querySelector('[role="dialog"][data-state="open"]');
}
async function upload(file) {
  const input = document.querySelector(
    'input[aria-label="Upload profile photo"]',
  );
  Object.defineProperty(input, "files", { configurable: true, value: [file] });
  await React.act(async () =>
    input.dispatchEvent(new window.Event("change", { bubbles: true })),
  );
}

test("personal settings use a compact dialog and retain Theme, Help, workspace and focus", async () => {
  await withDOM(async ({ render, window }) => {
    const { ProfileMenu, Theme } = await components();
    const storage = memory();
    const previous = globalThis.fetch;
    globalThis.fetch = () => {
      throw Error("Personal settings must not mutate a Book.");
    };
    try {
      await render(
        React.createElement(
          Theme,
          { initialTheme: "light", initialPreference: "light" },
          React.createElement(ProfileMenu, { storage }),
          React.createElement(
            "main",
            null,
            React.createElement("input", {
              "aria-label": "Book draft",
              defaultValue: "Retain me",
            }),
          ),
        ),
      );
      const main = document.querySelector("main"),
        draft = main.querySelector("input");
      const trigger = document.querySelector(
        'button[aria-label="Open profile menu"]',
      );
      const dialog = await open();
      assert.ok(dialog.classList.contains("personal-settings-dialog"));
      assert.equal(
        dialog.querySelector(".dialog-heading h2").textContent,
        "Personal settings",
      );
      assert.equal(dialog.querySelector(".dialog-heading p"), null);
      assert.equal(dialog.getAttribute("aria-describedby"), null);
      assert.equal(
        dialog.querySelector('input[aria-label="Display name"]').value,
        "",
      );
      assert.equal(document.querySelector('[role="menuitem"]'), null);
      assert.equal(dialog.querySelector('[role="status"]').textContent, "");
      assert.equal(dialog.querySelector('button[type="submit"]'), null);
      await click(find('[role="tab"]', "Preferences"));
      const select = dialog.querySelector(
        'select[aria-label="Theme preference"]',
      );
      assert.deepEqual(
        [...select.options].map((o) => o.value),
        ["system", "light", "dark"],
      );
      await React.act(async () => {
        select.value = "dark";
        select.dispatchEvent(new window.Event("change", { bubbles: true }));
      });
      assert.equal(
        document.documentElement.dataset.vultThemePreference,
        "dark",
      );
      await click(find('[role="tab"]', "Profile"));
      assert.equal(document.querySelector("main"), main);
      assert.equal(draft.value, "Retain me");
      await click(dialog.querySelector('button[aria-label="Close dialog"]'));
      assert.equal(document.activeElement, trigger);
      await click(trigger);
      assert.ok(find('[role="menuitem"]', "Help & documentation"));
      assert.ok(
        [...document.querySelectorAll('[role="menuitem"]')].some((n) =>
          n.textContent.includes("Appearance"),
        ),
      );
      assert.equal(find('[role="menuitem"]', "Settings"), undefined);
    } finally {
      globalThis.fetch = previous;
    }
  });
});

test("photo replacement, removal and display-name persistence update personal identity only", async () => {
  await withDOM(async ({ render }) => {
    const { ProfileMenu, Theme, createProfileStore } = await components();
    const storage = memory();
    const scope = { kind: "browser" };
    createProfileStore(scope, storage).update({
      display_name: "Taylor Example",
    });
    await render(
      React.createElement(
        Theme,
        { initialTheme: "light", initialPreference: "light" },
        React.createElement(ProfileMenu, {
          storage,
          decodePhoto: async () => photo,
        }),
      ),
    );
    const dialog = await open();
    assert.equal(
      dialog.querySelector('input[aria-label="Display name"]').value,
      "Taylor Example",
    );
    await upload({ name: "synthetic.jpg" });
    assert.equal(
      createProfileStore(scope, storage).getSnapshot().value.photo,
      photo,
    );
    assert.equal(
      document
        .querySelector('button[aria-label="Open profile menu"] img')
        .getAttribute("src"),
      photo,
    );
    await click(find("button", "Remove"));
    assert.equal(
      createProfileStore(scope, storage).getSnapshot().value.photo,
      null,
    );
    assert.equal(
      document.querySelector('button[aria-label="Open profile menu"] img'),
      null,
    );
  });
});

test("late photo decoding is discarded after closing or changing User scope", async () => {
  await withDOM(async ({ render }) => {
    const { ProfileMenu, Theme, createProfileStore } = await components();
    const storage = memory();
    let resolve;
    const decodePhoto = () =>
      new Promise((r) => {
        resolve = r;
      });
    const a = { kind: "user", id: "synthetic-a" },
      b = { kind: "user", id: "synthetic-b" };
    const tree = (scope) =>
      React.createElement(
        Theme,
        { initialTheme: "light", initialPreference: "light" },
        React.createElement(ProfileMenu, { scope, storage, decodePhoto }),
      );
    await render(tree(a));
    let dialog = await open();
    await upload({ name: "synthetic.jpg" });
    await render(tree(b));
    await React.act(async () => resolve(photo));
    assert.equal(
      createProfileStore(a, storage).getSnapshot().value.photo,
      null,
    );
    assert.equal(
      createProfileStore(b, storage).getSnapshot().value.photo,
      null,
    );
    await upload({ name: "synthetic.jpg" });
    dialog = document.querySelector('[role="dialog"][data-state="open"]');
    await click(dialog.querySelector('button[aria-label="Close dialog"]'));
    await React.act(async () => resolve(photo));
    assert.equal(
      createProfileStore(b, storage).getSnapshot().value.photo,
      null,
    );
  });
});

test("photo errors and blocked persistence remain visible without closing the profile", async () => {
  await withDOM(async ({ render }) => {
    const { ProfileMenu, Theme } = await components();
    await render(
      React.createElement(
        Theme,
        { initialTheme: "light", initialPreference: "light" },
        React.createElement(ProfileMenu, {
          storage: null,
          decodePhoto: async () => {
            throw Error("Choose a valid PNG or JPEG photo.");
          },
        }),
      ),
    );
    const dialog = await open();
    await upload({ name: "bad.svg" });
    assert.match(
      dialog.querySelector('[role="alert"]').textContent,
      /valid PNG or JPEG/,
    );
    assert.match(
      dialog.querySelector('[role="status"]').textContent,
      /could not be saved/,
    );
    assert.ok(document.querySelector('[role="dialog"][data-state="open"]'));
  });
});
