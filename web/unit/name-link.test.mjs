import assert from "node:assert/strict";
import test from "node:test";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

test("shared name links keep avatars/icons/metadata outside the name and preserve native actions", async () => {
  await withDOM(async ({ render }) => {
    const { NameLink } = await loadComponent("src/components/ui/name-link.tsx");
    let actions = 0,
      parent = 0;
    await render(
      React.createElement(
        "div",
        { onClick: () => parent++ },
        React.createElement(NameLink, {
          name: "Operating",
          leading: React.createElement("span", { "aria-hidden": true }, "OP"),
          trailing: React.createElement("svg", { "aria-hidden": true }),
          onClick: () => actions++,
        }),
      ),
    );
    const button = document.querySelector("button");
    const label = button.querySelector(".vult-name-link-label");
    assert.equal(label.textContent, "Operating");
    assert.equal(label.querySelector("svg, [aria-hidden]"), null);
    assert.equal(
      button.querySelectorAll(".vult-name-link-decoration").length,
      2,
    );
    await React.act(async () => button.click());
    assert.equal(actions, 1);
    assert.equal(
      parent,
      0,
      "an identity press must not toggle its enclosing selection",
    );
    await render(
      React.createElement(NameLink, {
        name: "Operating",
        disabled: true,
        onClick: () => actions++,
      }),
    );
    await React.act(async () => document.querySelector("button").click());
    assert.equal(actions, 1);
  });
});

test("the anchor form preserves navigation attributes and refs", async () => {
  await withDOM(async ({ render }) => {
    const { NameLink } = await loadComponent("src/components/ui/name-link.tsx");
    const ref = React.createRef();
    await render(
      React.createElement(NameLink, {
        name: "Original",
        href: "/original",
        target: "_blank",
        rel: "noreferrer",
        title: "Original file",
        ref,
        trailing: React.createElement("svg"),
      }),
    );
    const anchor = document.querySelector("a");
    assert.equal(ref.current, anchor);
    assert.equal(anchor.getAttribute("href"), "/original");
    assert.equal(anchor.target, "_blank");
    assert.equal(anchor.rel, "noreferrer");
    assert.equal(anchor.title, "Original file");
    assert.equal(
      anchor.querySelector(".vult-name-link-label").textContent,
      "Original",
    );
  });
});

test("the build guard rejects new bare record links and page-owned hover underlines", () => {
  const dir = mkdtempSync(join(tmpdir(), "vult-name-links-"));
  const run = () =>
    spawnSync(
      process.execPath,
      [resolve("scripts/check-name-links.mjs"), dir],
      { encoding: "utf8" },
    );
  try {
    mkdirSync(join(dir, "src/components"), { recursive: true });
    mkdirSync(join(dir, "src/styles"), { recursive: true });
    const page = join(dir, "src/components/page.tsx");
    writeFileSync(
      page,
      'const A=()=> <NameLink className="entry-link" name="Example"/>;',
    );
    assert.equal(run().status, 0);
    writeFileSync(
      page,
      'const A=()=> <button className="entry-link">Example</button>;',
    );
    assert.notEqual(run().status, 0);
    writeFileSync(
      page,
      'const A=()=> <NameLink className="entry-link" name="Example"/>;',
    );
    writeFileSync(
      join(dir, "src/styles/page.css"),
      ".entry-link:hover {text-decoration:underline}",
    );
    assert.notEqual(run().status, 0);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
