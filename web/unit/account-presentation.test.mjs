import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";
import { activityResponse, activitySeries } from "./helpers/activity.mjs";

const catalogText = await readFile(
  new URL("./fixtures/shared-assets/catalog.v1.json", import.meta.url),
  "utf8",
);
const book = {
  id: "presentation-book",
  name: "Synthetic",
  currency: "USD",
  scale: 2,
  start_date: "2026-01-01",
  accounting_today: "2026-10-25",
  accounting_timezone: "UTC",
};
const accounts = [
  {
    code: "a",
    name: "Checking •• 1111",
    kind: "asset",
    usage: "cash",
    currency: "USD",
    scale: 2,
    balance_minor: "5000",
    native_minor: "5000",
    posting_count: 2,
  },
  {
    code: "b",
    name: "Savings •• 2222",
    kind: "asset",
    usage: "cash",
    currency: "USD",
    scale: 2,
    balance_minor: "3000",
    native_minor: "3000",
    posting_count: 1,
  },
];
const flush = (ms = 30) =>
  React.act(async () => new Promise((resolve) => setTimeout(resolve, ms)));
async function clickText(text) {
  const node = [...document.querySelectorAll('button,[role="menuitem"]')].find(
    (node) => node.textContent === text,
  );
  assert.ok(node, `Missing control ${text}`);
  await React.act(async () => node.click());
}
async function openEditor(name) {
  const trigger = [...document.querySelectorAll("button[aria-label]")].find(
    (node) => node.getAttribute("aria-label") === `Customize account ${name}`,
  );
  assert.ok(trigger, `Missing direct customization control for ${name}`);
  await React.act(async () => {
    trigger.focus();
    trigger.click();
  });
  assert.ok(document.querySelector('[role="dialog"]'));
  assert.equal(document.querySelector('[role="menu"]'), null);
  assert.match(
    document.querySelector('[role="dialog"]').textContent,
    /Customize account/,
  );
}
async function setAlias(window, value) {
  await React.act(async () => {
    const input = document.querySelector('input[name="alias"]');
    Object.getOwnPropertyDescriptor(
      window.HTMLInputElement.prototype,
      "value",
    ).set.call(input, value);
    input.dispatchEvent(new window.Event("input", { bubbles: true }));
  });
}
function history(id) {
  return activityResponse(
    { ...book, id },
    {
      a: { weekly: activitySeries(2), total: 2, last_date: "2026-10-25" },
      b: { weekly: activitySeries(1), total: 1, last_date: "2026-10-25" },
    },
  );
}

test("shared institution references survive moving the same original to a central origin", async () => {
  const { sharedAssetOrigin, parseInstitutionCatalog, institutionAssetURL } =
    await loadComponent("src/lib/shared-institutions.ts");
  const catalog = parseInstitutionCatalog(catalogText);
  assert.equal(catalog.institutions.length, 2);
  assert.equal(sharedAssetOrigin("/assets/shared"), "");
  assert.equal(sharedAssetOrigin("/assets/shared", true), "/assets/shared");
  for (const origin of [
    "http://assets.example",
    "https://assets.example/path",
    "https://assets.example;evil",
    "https://user:secret@assets.example",
    "https://127.0.0.1",
    "https://localhost",
  ])
    assert.equal(sharedAssetOrigin(origin), "");
  assert.equal(
    sharedAssetOrigin("https://assets.example"),
    "https://assets.example",
  );
  for (const item of catalog.institutions)
    assert.equal(
      institutionAssetURL("/assets/shared", item).replace(
        "/assets/shared/",
        "",
      ),
      institutionAssetURL("https://assets.example", item).replace(
        "https://assets.example/",
        "",
      ),
    );
  const bad = JSON.parse(catalogText);
  bad.institutions[0].asset.sha256 = "../../secret";
  assert.throws(() => parseInstitutionCatalog(JSON.stringify(bad)), /invalid/);
  bad.institutions = [catalog.institutions[0], catalog.institutions[0]];
  assert.throws(() => parseInstitutionCatalog(JSON.stringify(bad)), /invalid/);
  bad.institutions = [
    {
      ...catalog.institutions[0],
      source_url: "https://unrelated.example/icon.png",
      source_page: "https://unrelated.example/",
    },
  ];
  assert.throws(
    () => parseInstitutionCatalog(JSON.stringify(bad)),
    /provenance/,
  );
});

test("brand presentation stays bounded and cannot inject CSS or turn a raster canvas into a mark", async () => {
  const { parseInstitutionCatalog, institutionAssetKey } = await loadComponent(
    "src/lib/shared-institutions.ts",
  );
  const valid = JSON.parse(catalogText);
  assert.ok(
    valid.institutions.every((item) =>
      institutionAssetKey(item).endsWith(".svg"),
    ),
  );
  for (const tamper of [
    (item) => {
      item.presentation.inset = 8;
    },
    (item) => {
      item.presentation.size = 32;
    },
    (item) => {
      item.presentation.scale = 1.2;
    },
    (item) => {
      item.presentation.light.padding = 2;
    },
    (item) => {
      item.presentation.inset = -1;
    },
    (item) => {
      item.presentation.inset = 28;
    },
    (item) => {
      item.presentation.inset = "4px";
    },
    (item) => {
      item.presentation.dark.foreground =
        'url("https://unrelated.example/pixel")';
    },
    (item) => {
      item.presentation.light.background = "var(--unverified)";
    },
    (item) => {
      delete item.presentation.dark;
    },
  ]) {
    const bad = JSON.parse(catalogText);
    tamper(bad.institutions[0]);
    assert.throws(
      () => parseInstitutionCatalog(JSON.stringify(bad)),
      /presentation is invalid/,
    );
  }
  // Official CDN originals retain a verified merchant page as provenance.
  const cdn = JSON.parse(catalogText);
  cdn.institutions[0].source_url = "https://cdn.example/official.svg";
  assert.ok(parseInstitutionCatalog(JSON.stringify(cdn)));
  // Full-color SVGs and approved static rasters share the image renderer.
  valid.institutions[0].presentation.mode = "image";
  assert.ok(parseInstitutionCatalog(JSON.stringify(valid)));
  for (const mode of ["image", "monochrome"])
    for (const media of ["image/png", "image/jpeg", "image/webp"]) {
      const raster = JSON.parse(catalogText);
      raster.institutions[0].asset.media_type = media;
      raster.institutions[0].presentation.mode = mode;
      if (mode === "image" && media !== "image/jpeg")
        assert.ok(parseInstitutionCatalog(JSON.stringify(raster)));
      else
        assert.throws(
          () => parseInstitutionCatalog(JSON.stringify(raster)),
          media === "image/jpeg" ? /catalog is invalid/ : /presentation is invalid/,
        );
    }
});

test("aliases retain ledger identity, logos reuse one original, and custom row order persists within their Book", async () => {
  await withDOM(async ({ render, window }) => {
    document.head.innerHTML =
      '<meta name="vult-shared-asset-origin" content="/assets/shared"><meta name="vult-development-assets" content="1">';
    const { AccountActivityList } = await loadComponent(
      "src/components/workspaces/account-activity-list.tsx",
    );
    const original = globalThis.fetch,
      calls = [],
      opened = [];
    globalThis.fetch = async (url, init) => {
      calls.push({ url: String(url), init });
      return String(url).endsWith("catalog.v1.json")
        ? new Response(catalogText, {
            headers: { "Content-Type": "application/json" },
          })
        : history(new URL(url, "http://localhost").searchParams.get("book_id"));
    };
    const view = (selected) =>
      React.createElement(AccountActivityList, {
        accounts,
        book: selected,
        refreshKey: "display",
        onOpen: (range) => opened.push(range),
      });
    const rows = () => [...document.querySelectorAll("tr[data-account-code]")];
    try {
      await render(view(book));
      await flush();
      assert.equal(
        document.querySelectorAll("[data-account-avatar]").length,
        accounts.length,
      );
      await openEditor(accounts[0].name);
      await setAlias(window, "Discard this draft");
      await React.act(async () =>
        document.querySelector('[aria-label="Close dialog"]').click(),
      );
      await flush(200);
      assert.equal(
        document.activeElement?.getAttribute("aria-label"),
        `Customize account ${accounts[0].name}`,
      );
      assert.doesNotMatch(rows()[0].textContent, /Discard this draft/);
      for (const [index, alias] of [
        [0, "Operating"],
        [1, "Tax reserve"],
      ]) {
        await openEditor(accounts[index].name);
        await setAlias(window, alias);
        const select = document.querySelector(
          'select[aria-label="Institution"]',
        );
        await React.act(async () => {
          select.value = "mercury";
          select.dispatchEvent(new window.Event("change", { bubbles: true }));
        });
        await clickText("Save changes");
        await flush(200);
        assert.equal(
          document.activeElement?.getAttribute("aria-label"),
          `Customize account ${accounts[index].name}`,
          "focus returns to the same direct customization trigger",
        );
      }
      const logos = [...document.querySelectorAll(".institution-logo img")];
      assert.equal(logos.length, 2);
      assert.equal(logos[0].src, logos[1].src);
      assert.equal(logos[0].crossOrigin, "anonymous");
      assert.match(rows()[0].textContent, /Operating.*Checking.*\$50\.00/);
      assert.match(
        rows()[0]
          .querySelector(".account-activity-name")
          .getAttribute("aria-label"),
        /Operating \(Checking •• 1111\)/,
      );
      const grip = rows()[1].querySelector(".account-row-grip");
      const key = async (value) =>
        React.act(async () =>
          grip.dispatchEvent(
            new window.KeyboardEvent("keydown", {
              key: value,
              bubbles: true,
              cancelable: true,
            }),
          ),
        );
      await key(" ");
      await key("ArrowUp");
      assert.equal(
        rows()[0].dataset.accountCode,
        "b",
        "keyboard movement previews the drop",
      );
      await key("Enter");
      await flush();
      assert.equal(
        document.querySelector('select[aria-label="Sort accounts"]').value,
        "custom",
      );
      assert.deepEqual(
        rows().map((row) => row.dataset.accountCode),
        ["b", "a"],
      );
      const raw = localStorage.getItem(
        `vult.account-presentation.v1:${book.id}`,
      );
      assert.match(raw, /mercury/);
      assert.doesNotMatch(raw, /https:|sha256|data:image|base64/);
      await render(null);
      await render(view(book));
      await flush();
      assert.deepEqual(
        rows().map((row) => row.dataset.accountCode),
        ["b", "a"],
      );
      await React.act(async () =>
        rows()[1].querySelector(".account-activity-name").click(),
      );
      assert.equal(opened[0].account, "a");
      await render(view({ ...book, id: "other-book" }));
      await flush();
      assert.doesNotMatch(
        document.querySelector(".account-activity-body").textContent,
        /Operating|Tax reserve|Pinned/,
      );
      assert.equal(document.querySelectorAll(".institution-logo").length, 0);
      assert.equal(
        document.querySelectorAll("[data-account-avatar]").length,
        accounts.length,
      );
      const reads = calls.filter((call) =>
        call.url.endsWith("catalog.v1.json"),
      );
      assert.equal(reads.length, 1);
      assert.equal(reads[0].init.credentials, "omit");
      assert.equal(reads[0].init.referrerPolicy, "no-referrer");
      assert.ok(
        calls.every((call) => !call.init?.method || call.init.method === "GET"),
      );
    } finally {
      globalThis.fetch = original;
    }
  });
});

test("blocked storage retains the editor and alias draft without claiming it saved", async () => {
  await withDOM(async ({ render, window }) => {
    const { AccountActivityList } = await loadComponent(
      "src/components/workspaces/account-activity-list.tsx",
    );
    const original = globalThis.fetch,
      setItem = window.Storage.prototype.setItem;
    globalThis.fetch = async () => history(book.id);
    window.Storage.prototype.setItem = () => {
      throw new Error("Quota exceeded");
    };
    try {
      await render(
        React.createElement(AccountActivityList, {
          accounts,
          book,
          refreshKey: "blocked",
        }),
      );
      await flush();
      await openEditor(accounts[0].name);
      await setAlias(window, "Operating");
      await clickText("Save changes");
      assert.ok(document.querySelector('[role="dialog"]'));
      assert.match(
        document.querySelector('[role="alert"]').textContent,
        /could not be saved/,
      );
      assert.equal(
        document.querySelector('input[name="alias"]').value,
        "Operating",
      );
      assert.equal(
        localStorage.getItem(`vult.account-presentation.v1:${book.id}`),
        null,
      );
    } finally {
      globalThis.fetch = original;
      window.Storage.prototype.setItem = setItem;
    }
  });
});

test("filtered custom ordering preserves hidden positions and migrates earlier pin references", async () => {
  const { reorderVisibleAccounts, parseAccountPresentation } =
    await loadComponent("src/lib/account-presentation.ts");
  assert.deepEqual(
    reorderVisibleAccounts(
      ["a", "hidden", "b", "other", "c"],
      ["a", "b", "c"],
      "c",
      0,
    ),
    ["c", "hidden", "a", "other", "b"],
  );
  const migrated = parseAccountPresentation(
    JSON.stringify({ version: 1, accounts: {}, pins: ["b", "a"] }),
  );
  assert.deepEqual(migrated.order, ["b", "a"]);
  assert.equal("pins" in migrated, false);
  assert.throws(
    () =>
      parseAccountPresentation(
        JSON.stringify({ version: 1, accounts: {}, order: ["a", "a"] }),
      ),
    /order/,
  );
});

test("pointer reordering saves, cancellation rolls back, and a changed Book cancels an unfinished move", async () => {
  await withDOM(async ({ render, window }) => {
    const { AccountActivityList } = await loadComponent(
      "src/components/workspaces/account-activity-list.tsx",
    );
    const original = globalThis.fetch;
    globalThis.fetch = async (url) =>
      history(new URL(url, "http://localhost").searchParams.get("book_id"));
    const view = (selected) =>
      React.createElement(AccountActivityList, {
        accounts,
        book: selected,
        refreshKey: "drag",
      });
    const rows = () => [...document.querySelectorAll("[data-account-code]")];
    const codes = () => rows().map((row) => row.dataset.accountCode);
    const pointer = async (target, type, y) =>
      React.act(async () => {
        const event = new window.MouseEvent(type, {
          bubbles: true,
          button: 0,
          clientY: y,
        });
        Object.defineProperties(event, {
          pointerId: { value: 1 },
          isPrimary: { value: true },
        });
        target.dispatchEvent(event);
      });
    const key = async (target, value) =>
      React.act(async () =>
        target.dispatchEvent(
          new window.KeyboardEvent("keydown", {
            key: value,
            bubbles: true,
            cancelable: true,
          }),
        ),
      );
    try {
      await render(view(book));
      await flush();
      const page = document.querySelector(".accounts-activity-page");
      page.getBoundingClientRect = () => ({ top: 0, bottom: 600 });
      rows().forEach(
        (row, index) =>
          (row.getBoundingClientRect = () => ({
            y: 100 + index * 76,
            height: 76,
          })),
      );
      const grip = rows()[1].querySelector(".account-row-grip");
      page.setPointerCapture = () => {};
      page.hasPointerCapture = () => false;
      await pointer(grip, "pointerdown", 214);
      assert.notEqual(
        document.activeElement,
        grip,
        "pointer dragging does not force keyboard focus",
      );
      await pointer(grip, "pointermove", 138);
      assert.deepEqual(codes(), ["b", "a"]);
      assert.equal(
        localStorage.getItem(`vult.account-presentation.v1:${book.id}`),
        null,
        "a preview does not claim it saved",
      );
      await pointer(grip, "pointerup", 138);
      assert.deepEqual(
        JSON.parse(
          localStorage.getItem(`vult.account-presentation.v1:${book.id}`),
        ).order,
        ["b", "a"],
      );
      const moved = rows()[0].querySelector(".account-row-grip");
      await key(moved, " ");
      await key(moved, "End");
      assert.deepEqual(codes(), ["a", "b"]);
      await key(moved, "Escape");
      assert.deepEqual(codes(), ["b", "a"]);
      const setItem = window.Storage.prototype.setItem;
      window.Storage.prototype.setItem = () => {
        throw new Error("Quota exceeded");
      };
      try {
        await key(moved, " ");
        await key(moved, "End");
        await key(moved, "Enter");
        assert.deepEqual(
          codes(),
          ["b", "a"],
          "failed persistence restores the saved order",
        );
        assert.match(
          document.querySelector('[role="alert"]').textContent,
          /could not be saved/,
        );
      } finally {
        window.Storage.prototype.setItem = setItem;
      }
      await key(moved, " ");
      await key(moved, "End");
      await render(view({ ...book, id: "other-drag-book" }));
      await flush();
      assert.deepEqual(codes(), ["a", "b"]);
      assert.equal(document.querySelector("[data-reordering]"), null);
      assert.equal(
        localStorage.getItem("vult.account-presentation.v1:other-drag-book"),
        null,
      );
    } finally {
      globalThis.fetch = original;
    }
  });
});

test("one continuous drag survives reordered handle capture loss and direction changes until pointer release", async () => {
  await withDOM(async ({ render, window }) => {
    const { AccountActivityList } = await loadComponent(
      "src/components/workspaces/account-activity-list.tsx",
    );
    const original = globalThis.fetch;
    globalThis.fetch = async () => history(book.id);
    const three = [
      ...accounts,
      { ...accounts[1], code: "c", name: "Third account" },
    ];
    const pointer = async (target, type, y, pointerId = 1) =>
      React.act(async () => {
        const event = new window.MouseEvent(type, {
          bubbles: true,
          cancelable: true,
          button: 0,
          clientY: y,
        });
        Object.defineProperties(event, {
          pointerId: { value: pointerId },
          isPrimary: { value: pointerId === 1 },
        });
        target.dispatchEvent(event);
      });
    const codes = () =>
      [...document.querySelectorAll("[data-account-code]")].map(
        (row) => row.dataset.accountCode,
      );
    try {
      await render(
        React.createElement(AccountActivityList, {
          accounts: three,
          book,
          refreshKey: "capture-loss",
        }),
      );
      await flush();
      const page = document.querySelector(".accounts-activity-page");
      page.getBoundingClientRect = () => ({ top: 0, bottom: 600 });
      const rows = [...document.querySelectorAll("[data-account-code]")];
      rows.forEach(
        (row, index) =>
          (row.getBoundingClientRect = () => ({
            y: 100 + index * 76,
            height: 76,
            top: 100 + index * 76,
          })),
      );
      const moved = rows[0],
        grip = moved.querySelector(".account-row-grip");
      let captured = null;
      for (const element of [grip, page]) {
        element.setPointerCapture = () => {
          captured = element;
        };
        element.hasPointerCapture = () => captured === element;
        element.releasePointerCapture = () => {
          captured = null;
        };
      }
      await pointer(grip, "pointerdown", 138);
      await pointer(captured, "pointermove", 290);
      assert.deepEqual(codes(), ["b", "c", "a"]);
      // Browsers release capture from a DOM element that is moved during reorder.
      await pointer(grip, "lostpointercapture", 290);
      assert.ok(
        document.querySelector("[data-reordering]"),
        "the grab must survive a moved handle losing capture",
      );
      assert.equal(
        captured,
        page,
        "the stable list owns capture across row moves",
      );
      await pointer(page, "pointercancel", 290, 2);
      assert.ok(
        document.querySelector("[data-reordering]"),
        "a second touch cannot cancel the active grab",
      );
      await pointer(captured, "pointermove", 214);
      assert.deepEqual(codes(), ["b", "a", "c"]);
      await pointer(captured, "pointermove", 138);
      assert.deepEqual(codes(), ["a", "b", "c"]);
      await pointer(captured, "pointermove", 290);
      assert.deepEqual(codes(), ["b", "c", "a"]);
      assert.equal(
        document.querySelector('[data-account-code="a"]'),
        moved,
        "the same row remains under the pointer",
      );
      assert.equal(
        localStorage.getItem(`vult.account-presentation.v1:${book.id}`),
        null,
        "crossing rows must never commit a drop",
      );
      await pointer(captured, "pointerup", 290);
      assert.equal(document.querySelector("[data-reordering]"), null);
      assert.deepEqual(
        JSON.parse(
          localStorage.getItem(`vult.account-presentation.v1:${book.id}`),
        ).order,
        ["b", "c", "a"],
      );
      assert.equal(captured, null, "release capture after the actual drop");
    } finally {
      globalThis.fetch = original;
    }
  });
});

test("a stationary edge hold traverses the whole list in one grab and stops at real content bounds", async () => {
  await withDOM(async ({ render, window }) => {
    const { AccountActivityList } = await loadComponent(
      "src/components/workspaces/account-activity-list.tsx",
    );
    const originalFetch = globalThis.fetch,
      originalFrame = globalThis.requestAnimationFrame,
      originalCancel = globalThis.cancelAnimationFrame;
    const positions = Array.from({ length: 20 }, (_, index) => ({
      ...accounts[0],
      code: `edge-${String(index).padStart(2, "0")}`,
      name: `Edge ${String(index).padStart(2, "0")}`,
    }));
    globalThis.fetch = async () =>
      activityResponse(
        book,
        Object.fromEntries(
          positions.map((a) => [
            a.code,
            { weekly: activitySeries(1), total: 1, last_date: "2026-10-25" },
          ]),
        ),
      );
    const callbacks = new Map();
    let nextFrame = 0,
      time = 0,
      scrollTop = 0,
      captured = false;
    const pointer = async (target, type, y) =>
      React.act(async () => {
        const event = new window.MouseEvent(type, {
          bubbles: true,
          cancelable: true,
          button: 0,
          clientY: y,
        });
        Object.defineProperties(event, {
          pointerId: { value: 1 },
          isPrimary: { value: true },
        });
        target.dispatchEvent(event);
      });
    const advance = async (count) => {
      for (let i = 0; i < count; i++)
        await React.act(async () => {
          time += 1000 / 60;
          const pending = [...callbacks.values()];
          callbacks.clear();
          pending.forEach((callback) => callback(time));
        });
    };
    try {
      await render(
        React.createElement(AccountActivityList, {
          accounts: positions,
          book,
          refreshKey: "edge-scroll",
        }),
      );
      await flush();
      const page = document.querySelector(".accounts-activity-page"),
        list = document.querySelector(".account-activity-body");
      const rows = [...list.querySelectorAll("[data-account-code]")],
        height = 76,
        header = 80,
        padding = 24,
        viewportHeight = 400;
      const trueHeight = header + rows.length * height + padding,
        maxScroll = trueHeight - viewportHeight;
      const codes = () =>
        [...list.querySelectorAll("[data-account-code]")].map(
          (row) => row.dataset.accountCode,
        );
      const rect = (y, h) => ({ y, top: y, bottom: y + h, height: h });
      page.getBoundingClientRect = () => rect(0, viewportHeight);
      list.getBoundingClientRect = () =>
        rect(header - scrollTop, rows.length * height);
      rows.forEach(
        (row) =>
          (row.getBoundingClientRect = () => {
            const offset = Number(
              row.style.transform.match(/translate3d\(0,\s*([-\d.]+)px/)?.[1] ||
                0,
            );
            return rect(
              header +
                codes().indexOf(row.dataset.accountCode) * height -
                scrollTop +
                offset,
              height,
            );
          }),
      );
      Object.defineProperties(page, {
        clientHeight: { configurable: true, value: viewportHeight },
        clientTop: { configurable: true, value: 0 },
        scrollHeight: {
          configurable: true,
          get: () =>
            Math.max(
              trueHeight,
              ...rows.map(
                (row) =>
                  row.getBoundingClientRect().bottom + scrollTop + padding,
              ),
            ),
        },
        scrollTop: {
          configurable: true,
          get: () => scrollTop,
          set: (value) => {
            scrollTop = Math.max(
              0,
              Math.min(value, page.scrollHeight - viewportHeight),
            );
          },
        },
      });
      page.style.paddingBottom = `${padding}px`;
      page.setPointerCapture = () => {
        captured = true;
      };
      page.hasPointerCapture = () => captured;
      page.releasePointerCapture = () => {
        captured = false;
      };
      globalThis.requestAnimationFrame = (callback) => {
        callbacks.set(++nextFrame, callback);
        return nextFrame;
      };
      globalThis.cancelAnimationFrame = (id) => callbacks.delete(id);
      const moved = rows[0],
        grip = moved.querySelector(".account-row-grip");
      await pointer(grip, "pointerdown", header + height / 2);
      await pointer(page, "pointermove", viewportHeight - 1);
      await advance(250);
      assert.ok(
        scrollTop <= maxScroll,
        "the dragged row must not manufacture an ever-growing blank tail",
      );
      assert.ok(
        Math.abs(scrollTop - maxScroll) < 1,
        "a stationary bottom-edge hold reaches the actual end",
      );
      assert.equal(codes().at(-1), positions[0].code);
      assert.ok(
        moved.getBoundingClientRect().bottom <= viewportHeight + 0.5,
        "the held row remains visible at the bottom boundary",
      );
      await advance(80);
      assert.ok(
        Math.abs(scrollTop - maxScroll) < 1,
        "holding past the end cannot add more scroll space",
      );
      await pointer(page, "pointermove", 1);
      await advance(250);
      assert.equal(
        scrollTop,
        0,
        "the same grab scrolls all the way back to the top",
      );
      assert.equal(codes()[0], positions[0].code);
      assert.ok(
        moved.getBoundingClientRect().top >= 0,
        "the row stays inside the top boundary",
      );
      assert.equal(captured, true);
      assert.ok(document.querySelector("[data-reordering]"));
      assert.equal(
        localStorage.getItem(`vult.account-presentation.v1:${book.id}`),
        null,
      );
      await pointer(page, "pointermove", viewportHeight - 1);
      await advance(250);
      await pointer(page, "pointerup", viewportHeight - 1);
      assert.equal(captured, false);
      assert.equal(
        JSON.parse(
          localStorage.getItem(`vult.account-presentation.v1:${book.id}`),
        ).order.at(-1),
        positions[0].code,
      );
      assert.equal(
        list.querySelectorAll("[data-account-code]").length,
        positions.length,
      );
    } finally {
      globalThis.fetch = originalFetch;
      globalThis.requestAnimationFrame = originalFrame;
      globalThis.cancelAnimationFrame = originalCancel;
    }
  });
});
