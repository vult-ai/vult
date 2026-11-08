import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

const { checkSource, checkTokens, tokens } = await import("../scripts/check-menu-standard.mjs");

test("screens cannot size menus or build them by hand", () => {
  for (const [source, rule] of [
    ['<Dropdown.Popover className="w-52">', /width follows its content/],
    ['<Dropdown.Popover placement="bottom" className="w-[148px] motion-reduce:animate-none">', /width follows its content/],
    ['<Popover className={cx("min-w-64")}>', /width follows its content/],
    ['<Select popoverClassName="w-44" />', /select list's width/],
    ['const submenu = { placement: "right top", offset: -4, className: "w-52 motion-reduce:animate-none" };', /width follows its content/],
    ['<div role="menu"><div role="menuitem">Edit</div></div>', /shared Dropdown or Select/],
  ])
    assert.match(checkSource("components/example.tsx", source).join("\n"), rule, source);
  assert.deepEqual(checkSource("components/example.tsx", '<Dropdown.Popover placement="bottom end" className="motion-reduce:animate-none">'), []);
  assert.deepEqual(checkSource("components/example.tsx", "document.querySelector('[role=\"listbox\"]')"), []);
  assert.deepEqual(checkSource("design-system/catalog/components/base/dropdown/dropdown.tsx", '<AriaPopover className="w-full">'), []);
});

test("the shared tokens keep the documented values", async () => {
  const css = await readFile(new URL("../src/design-system/styles/vult-controls.css", import.meta.url), "utf8");
  assert.deepEqual(checkTokens(css), []);
  assert.match(checkTokens(css.replace("--vult-menu-max-width: 280px", "--vult-menu-max-width: 300px")).join(""), /280px/);
  const doc = await readFile(new URL("../../docs/design/menus.md", import.meta.url), "utf8");
  for (const value of ["144px", "280px", "320px", "360px", "32px", "16px", "8px", "12px", "4px"])
    assert.match(doc, new RegExp(value), `the document states ${value}`);
  assert.equal(Object.keys(tokens).length, 10);
});

test("every menu row uses the standard geometry", () =>
  import("./helpers/dom.mjs").then(({ React, loadComponent, withDOM }) =>
    withDOM(async ({ render }) => {
      const { Dropdown } = await loadComponent("src/design-system/catalog/components/vult-ui/dropdown.tsx");
      await render(
        React.createElement(Dropdown.Root, { defaultOpen: true },
          React.createElement("button", null, "Open"),
          React.createElement(Dropdown.Popover, null,
            React.createElement(Dropdown.Menu, { "aria-label": "Actions" },
              React.createElement(Dropdown.Item, { id: "a", label: "Export transactions" }),
              React.createElement(Dropdown.Item, { id: "b", label: "Delete", isDestructive: true })))));
      const popover = document.querySelector(".vult-menu");
      assert.ok(popover, "the popover uses the content-sized menu surface");
      assert.doesNotMatch(popover.className, /(^|\s)w-/);
      assert.ok(document.querySelector(".vult-menu-list"));
      const rows = [...document.querySelectorAll(".vult-menu-row")];
      assert.equal(rows.length, 2);
      assert.equal(rows[0].querySelector("[title]").getAttribute("title"), "Export transactions");
    })));
