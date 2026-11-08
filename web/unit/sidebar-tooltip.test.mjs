import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent, React, withDOM } from "./helpers/dom.mjs";

async function fixture() {
  const { useRailTooltip } = await loadComponent(
    "src/components/shell/use-rail-tooltip.tsx",
  );
  return function Rail({ expanded = false }) {
    const { handlers, tooltip } = useRailTooltip(expanded);
    return React.createElement(
      React.Fragment,
      null,
      React.createElement(
        "aside",
        handlers,
        React.createElement(
          "a",
          { href: "/dashboard", "aria-label": "Dashboard" },
          "Home icon",
        ),
        React.createElement(
          "a",
          { href: "/accounts", "aria-label": "Accounts" },
          "Accounts icon",
        ),
        React.createElement(
          "button",
          { "aria-label": "Settings" },
          "Settings icon",
        ),
      ),
      tooltip,
      React.createElement("input", {
        "aria-label": "Workspace draft",
        defaultValue: "Keep this draft",
      }),
    );
  };
}
const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
async function pointer(
  window,
  node,
  type,
  { pointerType = "mouse", relatedTarget = null, delay = 0 } = {},
) {
  await React.act(async () => {
    const event = new window.MouseEvent(type, { bubbles: true, relatedTarget });
    Object.defineProperty(event, "pointerType", { value: pointerType });
    node.dispatchEvent(event);
    if (delay) await wait(delay);
  });
}
const tooltip = () => document.querySelector('[role="tooltip"]');

test("collapsed rail shares one tooltip for mouse scanning, keyboard focus and dismissal", async () => {
  await withDOM(async ({ render, window }) => {
    const Rail = await fixture();
    await render(React.createElement(Rail));
    const home = document.querySelector('a[aria-label="Dashboard"]'),
      accounts = document.querySelector('a[aria-label="Accounts"]'),
      settings = document.querySelector('button[aria-label="Settings"]'),
      draft = document.querySelector("input");
    await pointer(window, home, "pointerover", {
      pointerType: "touch",
      delay: 120,
    });
    assert.equal(tooltip(), null, "touch does not create hover help");
    await pointer(window, home, "pointerover");
    assert.equal(tooltip(), null, "initial hover keeps the 100ms delay");
    await React.act(async () => wait(120));
    const retained = tooltip();
    assert.ok(retained);
    assert.equal(retained.textContent, "Dashboard");
    assert.equal(retained.dataset.placement, "right");
    await pointer(window, home, "pointerout", { relatedTarget: accounts });
    await pointer(window, accounts, "pointerover");
    assert.equal(tooltip(), retained);
    assert.equal(tooltip().textContent, "Accounts");
    await React.act(async () =>
      window.dispatchEvent(
        new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      ),
    );
    assert.equal(tooltip(), null);
    await React.act(async () => settings.focus());
    assert.equal(tooltip().textContent, "Settings");
    assert.equal(document.activeElement, settings);
    await React.act(async () => draft.focus());
    assert.ok(tooltip(), "leave delay bridges small gaps");
    await React.act(async () => wait(160));
    assert.equal(tooltip(), null);
    assert.equal(draft.value, "Keep this draft");
  });
});

test("expanded/mobile rails suppress help and clicking preserves native navigation", async () => {
  await withDOM(async ({ render, window }) => {
    const Rail = await fixture();
    await render(React.createElement(Rail, { expanded: true }));
    const home = document.querySelector('a[aria-label="Dashboard"]');
    await React.act(async () => home.focus());
    assert.equal(tooltip(), null);
    await render(React.createElement(Rail, { expanded: false }));
    const accounts = document.querySelector('a[aria-label="Accounts"]');
    await React.act(async () => accounts.focus());
    assert.equal(tooltip().textContent, "Accounts");
    let prevented;
    accounts.addEventListener(
      "click",
      (event) => {
        prevented = event.defaultPrevented;
        event.preventDefault();
      },
      { once: true },
    );
    await React.act(async () =>
      accounts.dispatchEvent(
        new window.MouseEvent("click", {
          bubbles: true,
          cancelable: true,
          ctrlKey: true,
        }),
      ),
    );
    assert.equal(prevented, false);
    assert.equal(accounts.getAttribute("href"), "/accounts");
    assert.equal(tooltip(), null);
    const media = globalThis.matchMedia;
    globalThis.matchMedia = (query) => ({ ...media(query), matches: false });
    try {
      await React.act(async () => home.focus());
      assert.equal(tooltip(), null);
    } finally {
      globalThis.matchMedia = media;
    }
  });
});
