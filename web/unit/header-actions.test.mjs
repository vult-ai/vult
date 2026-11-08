import { phosphorPlugin } from "./helpers/phosphor-plugin.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { React, withDOM } from "./helpers/dom.mjs";
import { build } from "esbuild";
import { createRequire } from "node:module";
const require = createRequire(import.meta.url);

test("page-owned header actions precede Search, retain owner state and clean up on navigation", async () => {
  await withDOM(async ({ render, window }) => {
    const bundle = await build({
      stdin: {
        contents:
          'export {AppShell} from "./src/components/shell/app-shell"; export {HeaderActions} from "./src/components/shell/header-actions"; export {Theme} from "./src/design-system/theme";',
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
      require,
    );
    const { AppShell, HeaderActions, Theme } = module.exports;
    const Icon = () => null;
    function Controls({ active }) {
      const [count, setCount] = React.useState(0);
      return React.createElement(
        HeaderActions,
        { active },
        React.createElement(
          "button",
          {
            "data-period-control": true,
            onClick: () => setCount((c) => c + 1),
          },
          `Period ${count}`,
        ),
      );
    }
    function Fixture() {
      const [active, setActive] = React.useState("dashboard");
      return React.createElement(
        Theme,
        { initialTheme: "light", initialPreference: "light" },
        React.createElement(
          AppShell,
          {
            bookName: "Synthetic Book",
            ready: true,
            active,
            onSettings() {},
            onNavigate: setActive,
            search: React.createElement(
              "button",
              { "data-search-control": true },
              "Search",
            ),
            navigation: [
              { id: "dashboard", label: "Dashboard", icon: Icon, href: "/dashboard" },
              { id: "accounts", label: "Accounts", icon: Icon, href: "/accounts" },
            ],
          },
          React.createElement(Controls, { active: active === "dashboard" }),
          React.createElement("input", {
            "aria-label": "Retained draft",
            defaultValue: "Keep this",
          }),
        ),
      );
    }
    await render(React.createElement(Fixture));
    const field = document.querySelector('input[aria-label="Retained draft"]');
    const period = document.querySelector("[data-period-control]");
    const search = document.querySelector("[data-search-control]");
    assert.equal(period.closest("header") !== null, true);
    assert.ok(
      period.compareDocumentPosition(search) &
        window.Node.DOCUMENT_POSITION_FOLLOWING,
    );
    assert.equal(document.querySelectorAll("[data-period-control]").length, 1);
    await React.act(async () =>
      period.dispatchEvent(new window.MouseEvent("click", { bubbles: true })),
    );
    assert.equal(period.textContent, "Period 1");
    await React.act(async () =>
      document
        .querySelector('a[href="/accounts"]')
        .dispatchEvent(
          new window.MouseEvent("click", { bubbles: true, cancelable: true }),
        ),
    );
    assert.equal(document.querySelector("[data-period-control]"), null);
    assert.equal(
      document.querySelector('input[aria-label="Retained draft"]'),
      field,
    );
    await React.act(async () =>
      document
        .querySelector('a[href="/dashboard"]')
        .dispatchEvent(
          new window.MouseEvent("click", { bubbles: true, cancelable: true }),
        ),
    );
    assert.equal(
      document.querySelector("[data-period-control]").textContent,
      "Period 1",
    );
  });
});
