import { phosphorPlugin } from "./helpers/phosphor-plugin.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { build } from "esbuild";
import { createRequire } from "node:module";
import { React, withDOM } from "./helpers/dom.mjs";
async function components() {
  const b = await build({
    stdin: {
      contents:
        'export {PersonalSettingsHost} from "./components/shell/personal-settings-host";export {AppShell} from "./components/shell/app-shell";',
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
  new Function("module", "exports", "require", b.outputFiles[0].text)(
    m,
    m.exports,
    createRequire(import.meta.url),
  );
  return m.exports;
}
const click = async (n) => {
  assert.ok(n);
  await React.act(async () => {
    n.click();
    await new Promise(requestAnimationFrame);
  });
};
test("the authenticated host survives Book switching, saves Appearance and reloads the server profile", async () => {
  await withDOM(async ({ render, window }) => {
    const { PersonalSettingsHost, AppShell } = await components();
    const previous = globalThis.fetch;
    const id = "a".repeat(64);
    let writes = [];
    let offlineSession = false;
    let value = {
      user_id: id,
      revision: "1",
      profile: { version: 1, display_name: "Account Taylor", photo: null },
      preferences: {
        version: 1,
        language: "en",
        regional_format: "en-US",
        date_format: "standard",
        time_format: "24h",
        digit_grouping: true,
        negative_style: "minus",
      },
      appearance: "dark",
    };
    globalThis.fetch = async (url, options = {}) => {
      let body;
      if (String(url).endsWith("/session")) {
        if (offlineSession) throw Error("synthetic offline session probe");
        body = {
          mode: "protected-single",
          user_id: id,
          csrf: "session-bound",
        };
      } else {
        assert.equal(url, "/api/v1/me/settings");
        if (options.method === "POST") {
          const p = JSON.parse(options.body);
          writes.push(p);
          assert.equal(options.headers["X-CSRF-Token"], "session-bound");
          value = {
            ...value,
            appearance: p.appearance ?? value.appearance,
            revision: "2",
          };
        }
        body = value;
      }
      return { ok: true, json: async () => body };
    };
    function Fixture() {
      const [book, setBook] = React.useState("Book A");
      return React.createElement(
        PersonalSettingsHost,
        null,
        React.createElement(
          AppShell,
          {
            bookName: book,
            bookSwitcher: React.createElement(
              "button",
              {
                "aria-label": "Switch fixture Book",
                onClick: () => setBook(book === "Book A" ? "Book B" : "Book A"),
              },
              book,
            ),
            ready: true,
            active: "",
            navigation: [],
            onSettings() {},
          },
          React.createElement("input", {
            "aria-label": "Retained draft",
            defaultValue: "Keep this",
          }),
        ),
      );
    }
    try {
      await render(React.createElement(Fixture));
      await React.act(async () => {
        await new Promise(requestAnimationFrame);
      });
      assert.equal(
        document.documentElement.dataset.vultThemePreference,
        "dark",
      );
      const draft = document.querySelector(
        'input[aria-label="Retained draft"]',
      );
      await click(
        document.querySelector('button[aria-label="Open profile menu"]'),
      );
      assert.match(document.body.textContent, /Account Taylor/);
      await click(
        [...document.querySelectorAll('[role="menuitem"]')].find(
          (x) => x.textContent === "Profile & preferences",
        ),
      );
      assert.equal(
        document.querySelector('.personal-settings-dialog [role="status"]').textContent,
        "",
      );
      await click(document.querySelector('button[aria-label="Close dialog"]'));
      await click(
        document.querySelector('button[aria-label="Switch fixture Book"]'),
      );
      assert.equal(
        document.querySelector('input[aria-label="Retained draft"]'),
        draft,
      );
      offlineSession = true;
      await React.act(async () => {
        window.dispatchEvent(new window.Event("focus"));
        await new Promise(requestAnimationFrame);
      });
      assert.equal(
        document.querySelector('input[aria-label="Retained draft"]'),
        draft,
        "a failed identity probe must not remount the workspace",
      );
      offlineSession = false;
      await React.act(async () => {
        window.dispatchEvent(new window.Event("focus"));
        await new Promise(requestAnimationFrame);
      });
      await click(
        document.querySelector('button[aria-label="Open profile menu"]'),
      );
      assert.match(document.body.textContent, /Account Taylor/);
      await click(
        [...document.querySelectorAll('[role="menuitem"]')].find(
          (x) => x.textContent === "Profile & preferences",
        ),
      );
      await click(
        [...document.querySelectorAll('[role="tab"]')].find(
          (x) => x.textContent === "Preferences",
        ),
      );
      const select = document.querySelector(
        'select[aria-label="Theme preference"]',
      );
      await React.act(async () => {
        select.value = "light";
        select.dispatchEvent(new window.Event("change", { bubbles: true }));
        await new Promise((r) => setTimeout(r, 300));
      });
      assert.equal(value.appearance, "light");
      assert.equal(writes.length, 1);
      assert.equal(writes[0].expected_revision, "1");
      assert.equal(
        document.documentElement.dataset.vultThemePreference,
        "light",
      );
    } finally {
      globalThis.fetch = previous;
    }
  });
});
