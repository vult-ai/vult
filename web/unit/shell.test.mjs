import { phosphorPlugin } from "./helpers/phosphor-plugin.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { React, withDOM } from "./helpers/dom.mjs";

test("collapsing and navigating reuse the shell, workspace, field draft and table", async () => {
  await withDOM(async ({ render }) => {
    // Load one bundle so the fixture and shell share the same Theme context.
    const { build } = await import("esbuild");
    const { createRequire } = await import("node:module");
    const require = createRequire(import.meta.url);
    const bundle = await build({
      stdin: {
        contents:
          'export {AppShell} from "./src/components/shell/app-shell"; export {Theme} from "./src/design-system/theme"; export {RecordsTable} from "./src/design-system/catalog/components/application/table/records-table";',
        resolveDir: process.cwd(),
        loader: "tsx",
      },
      bundle: true,
      write: false,
      platform: "node",
      format: "cjs",
      packages: "external", plugins: [phosphorPlugin],
      loader: { ".module.css": "empty", ".css": "empty" },
    });
    const module = { exports: {} };
    new Function("module", "exports", "require", bundle.outputFiles[0].text)(
      module,
      module.exports,
      require,
    );
    const { AppShell: Shell, Theme: Provider, RecordsTable } = module.exports;
    const Icon = () => null;
    let unmounts = 0;
    function Workspace() {
      React.useEffect(() => () => unmounts++, []);
      return React.createElement(
        "div",
        null,
        React.createElement("input", {
          "aria-label": "Draft",
          defaultValue: "Keep this",
        }),
        React.createElement(RecordsTable, {
          ariaLabel: "Records",
          testId: "records",
          columns: [
            {
              id: "name",
              label: "Description",
              width: 200,
              minWidth: 100,
              isRowHeader: true,
            },
          ],
          rows: [{ id: "a", name: "Record" }],
          rowId: (row) => row.id,
          rowLabel: (row) => row.name,
          renderCell: (row) => row.name,
          selectionEnabled: false,
          emptyState: "Empty",
        }),
      );
    }
    function Fixture() {
      const [active, setActive] = React.useState("");
      return React.createElement(
        Provider,
        { initialTheme: "light", initialPreference: "light" },
        React.createElement(
          Shell,
          {
            bookName: "Synthetic Studio",
            ready: true,
            active,
            navigation: [
              { id: "", label: "All transactions", icon: Icon },
              { id: "income", label: "Income", icon: Icon },
            ],
            onNavigate: setActive,
            onSettings() {},
          },
          React.createElement(Workspace),
        ),
      );
    }
    await render(React.createElement(Fixture));
    const main = document.querySelector("main"),
      table = document.querySelector("table"),
      field = document.querySelector('input[aria-label="Draft"]'),
      navigation = document.querySelector('nav[aria-label="Book navigation"]');
    main.scrollTop = 160;
    field.value = "Unsubmitted draft";
    const collapse = document.querySelector(
      'button[aria-label="Collapse sidebar"]',
    );
    await React.act(async () => {
      collapse.focus();
      collapse.click();
    });
    assert.equal(document.querySelector("main"), main);
    assert.equal(document.querySelector("table"), table);
    assert.equal(document.querySelector('input[aria-label="Draft"]'), field);
    assert.equal(field.value, "Unsubmitted draft");
    assert.equal(main.scrollTop, 160);
    assert.equal(document.activeElement, collapse);
    assert.equal(document.querySelector("aside").dataset.expanded, "false");
    assert.equal(document.querySelector("aside").dataset.animate, "false");
    assert.equal(localStorage.getItem("vult:sidebar-expanded:v1"), "false");
    assert.ok(
      document.querySelector('nav[aria-label="Book navigation"]') ===
        navigation,
    );
    const space = document.querySelector(".vult-shell-rail-space");
    const settings = document.querySelector(
      '.vult-shell-rail-footer button[aria-label="Settings"]',
    );
    const settingsRow = settings.closest(".vult-navigation-item");
    assert.ok(settingsRow, "Settings retains its ordinary row");
    assert.equal(
      document.querySelectorAll(".vult-gliding-hover-surface").length,
      0,
    );
    assert.equal(space.dataset.expanded, "false");
    assert.equal(space.dataset.animate, "false");
    // A pointer toggle shares one state across the rail, its spacer and heading.
    await React.act(async () => {
      collapse.dispatchEvent(
        new MouseEvent("click", { bubbles: true, detail: 1 }),
      );
    });
    assert.equal(document.querySelector("aside").dataset.expanded, "true");
    assert.equal(document.querySelector("aside").dataset.animate, "true");
    assert.equal(space.dataset.expanded, "true");
    assert.equal(space.dataset.animate, "true");
    assert.ok(document.querySelector("main") === main);
    assert.ok(document.querySelector("table") === table);
    assert.ok(document.querySelector('input[aria-label="Draft"]') === field);
    assert.equal(field.value, "Unsubmitted draft");
    assert.equal(main.scrollTop, 160);
    assert.equal(document.activeElement, collapse);
    assert.equal(
      document.querySelector(
        '.vult-shell-rail-footer button[aria-label="Settings"]',
      ),
      settings,
    );
    assert.equal(settings.closest(".vult-navigation-item"), settingsRow);
    await React.act(async () =>
      [...document.querySelectorAll('nav[aria-label="Book navigation"] button')]
        .find((node) => node.textContent === "Income")
        .click(),
    );
    assert.equal(document.querySelector("table"), table);
    assert.equal(unmounts, 0);
    assert.equal(
      document.querySelector('[aria-label="Breadcrumb"]').textContent,
      "Income",
    );
  });
});
