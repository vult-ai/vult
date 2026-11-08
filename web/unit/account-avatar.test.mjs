import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { Resvg } from "@resvg/resvg-js";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";
import { institutionAssetKey } from "../src/lib/shared-institutions.ts";

const catalog = JSON.parse(
  await readFile(
    new URL("./fixtures/shared-assets/catalog.v1.json", import.meta.url),
    "utf8",
  ),
);

const rasters = new Map();
for (const item of catalog.institutions) {
  const svg = await readFile(
    new URL(
      `./fixtures/shared-assets/${institutionAssetKey(item)}`,
      import.meta.url,
    ),
  );
  rasters.set(
    item.asset.sha256,
    new Resvg(`<svg xmlns="http://www.w3.org/2000/svg" width="512" height="512"><image width="512" height="512" href="data:${item.asset.media_type};base64,${svg.toString("base64")}"/></svg>`, {
      font: { loadSystemFonts: false },
    }).render().pixels,
  );
}
// JSDOM has no canvas renderer. Feed the real unchanged SVGs' independently
// rendered pixels into the image-load boundary; the production fit still runs.
function mockMarkCanvas(window) {
  let reads = 0;
  Object.defineProperties(window.HTMLImageElement.prototype, {
    naturalWidth: { configurable: true, get: () => 512 },
    naturalHeight: { configurable: true, get: () => 512 },
  });
  window.HTMLCanvasElement.prototype.getContext = () => {
    let hash;
    return {
      drawImage(image) {
        hash = catalog.institutions.find((item) =>
          image.src.includes(item.asset.sha256),
        )?.asset.sha256;
      },
      getImageData() {
        reads++;
        return { data: rasters.get(hash) };
      },
    };
  };
  return () => reads;
}

test("account initials use the purpose or leaf name, including Unicode names", async () => {
  const { accountInitials } = await loadComponent(
    "src/components/ui/account-avatar.tsx",
  );
  for (const [name, initials] of [
    ["Bank fees & service charges", "BF"],
    ["Software & apps", "SA"],
    ["Operating", "OP"],
    ["Wages", "WA"],
    ["Łódź operations", "ŁO"],
    ["税 金", "税金"],
    ["••", "A"],
    ["ß reserve", "SS"],
  ])
    assert.equal(accountInitials(name), initials);
});

test("an account keeps its identity color when renamed, without extra focus targets", async () => {
  await withDOM(async ({ render }) => {
    const { AccountAvatar } = await loadComponent(
      "src/components/ui/account-avatar.tsx",
    );
    const view = (name) =>
      React.createElement(AccountAvatar, {
        accountKey: "book:bank-fees",
        name,
        base: "",
      });
    await render(view("Bank fees"));
    const circle = document.querySelector("[data-account-avatar]");
    const tone = circle.dataset.tone;
    assert.equal(circle.textContent, "BF");
    assert.equal(circle.getAttribute("aria-hidden"), "true");
    assert.equal(circle.querySelectorAll("img,button,a,[tabindex]").length, 0);
    await render(view("Operating"));
    assert.equal(document.querySelector("[data-account-avatar]"), circle);
    assert.equal(circle.dataset.tone, tone);
    assert.equal(circle.textContent, "OP");
  });
});

test("one shared original overlays initials only after load and failure keeps the avatar", async () => {
  await withDOM(async ({ render, window }) => {
    mockMarkCanvas(window);
    const { AccountAvatar } = await loadComponent(
      "src/components/ui/account-avatar.tsx",
    );
    const view = (institution, base = "https://assets.example") =>
      React.createElement(AccountAvatar, {
        accountKey: "book:checking",
        name: "Operating",
        base,
        institution,
      });
    await render(view(catalog.institutions[0]));
    const circle = document.querySelector("[data-account-avatar]");
    let image = circle.querySelector("img");
    assert.equal(
      image.src,
      `https://assets.example/sha256/${catalog.institutions[0].asset.sha256}.svg`,
    );
    assert.equal(image.crossOrigin, "anonymous");
    assert.equal(image.getAttribute("referrerpolicy"), "no-referrer");
    assert.equal(image.alt, "");
    const logo = circle.querySelector(".institution-logo");
    assert.equal(logo.dataset.render, "monochrome");
    assert.equal(
      logo.style.getPropertyValue("--institution-foreground-light"),
      "#272735",
    );
    assert.equal(
      logo.style.getPropertyValue("--institution-foreground-dark"),
      "#f4f5f9",
    );
    assert.equal(
      logo.style.getPropertyValue("--institution-source"),
      `url("${image.src}")`,
    );
    assert.ok(logo.querySelector(".institution-logo-mark"));
    assert.equal(circle.querySelector("[data-avatar]").textContent, "OP");
    assert.equal(
      circle.querySelector(".institution-logo").hasAttribute("data-loaded"),
      false,
    );
    await React.act(async () => image.dispatchEvent(new window.Event("load")));
    assert.equal(
      circle.querySelector(".institution-logo").hasAttribute("data-loaded"),
      true,
    );
    await render(view(catalog.institutions[1]));
    image = circle.querySelector("img");
    const wise = circle.querySelector(".institution-logo");
    assert.equal(
      wise.style.getPropertyValue("--institution-background-light"),
      "#87ea5c",
    );
    assert.equal(
      wise.style.getPropertyValue("--institution-background-dark"),
      "#87ea5c",
    );
    assert.equal(wise.style.getPropertyValue("--institution-inset"), "");
    assert.equal(
      circle.style.getPropertyValue("--account-mark-padding"),
      "8px",
    );
    assert.equal(
      circle.querySelector(".institution-logo").hasAttribute("data-loaded"),
      false,
    );
    await React.act(async () => image.dispatchEvent(new window.Event("error")));
    assert.equal(circle.querySelector("img"), null);
    assert.equal(circle.textContent, "OP");
    assert.equal(document.querySelector("[data-account-avatar]"), circle);
    await render(view(catalog.institutions[0], ""));
    assert.equal(circle.querySelector("img"), null);
    assert.equal(circle.textContent, "OP");
  });
});

test("one medium-avatar geometry survives brand, name, loading and theme changes", async () => {
  const { ACCOUNT_AVATAR_STANDARD } = await loadComponent(
    "src/design-system/account-avatar-standard.ts",
  );
  assert.deepEqual(ACCOUNT_AVATAR_STANDARD, {
    version: 2,
    sourceSize: "md",
    diameter: 40,
    markSize: 24,
    padding: 8,
    markShape: "circle",
    fit: "painted-radius",
    measurementSize: 512,
  });
  assert.equal(Object.isFrozen(ACCOUNT_AVATAR_STANDARD), true);
  await withDOM(async ({ render, window }) => {
    mockMarkCanvas(window);
    const { AccountAvatar } = await loadComponent(
      "src/components/ui/account-avatar.tsx",
    );
    let original;
    for (const [index, institution] of [
      undefined,
      ...catalog.institutions,
    ].entries()) {
      document.documentElement.className =
        index % 2 ? "light-mode" : "dark-mode";
      await render(
        React.createElement(AccountAvatar, {
          accountKey: "same-account",
          name: index ? "Operating" : "Checking",
          base: "https://assets.example",
          institution: institution && {
            ...institution,
            presentation: {
              ...institution.presentation,
              inset: 999,
              diameter: 99,
            },
          },
        }),
      );
      const avatar = document.querySelector("[data-account-avatar]");
      original ||= avatar;
      assert.equal(
        avatar,
        original,
        "identity is retained through a brand change",
      );
      assert.equal(avatar.dataset.avatarStandard, "2");
      assert.equal(
        avatar.style.getPropertyValue("--account-avatar-size"),
        "40px",
      );
      assert.equal(
        avatar.style.getPropertyValue("--account-mark-padding"),
        "8px",
      );
      assert.ok(
        avatar.querySelector("[data-avatar]").classList.contains("size-10"),
        "the actual pinned md Avatar owns the frame",
      );
      assert.ok(
        avatar
          .querySelector("[data-avatar] span")
          .classList.contains("text-md"),
        "initials retain the source md typography",
      );
      const image = avatar.querySelector("img");
      if (image) {
        assert.equal(image.width, 24);
        await React.act(async () =>
          image.dispatchEvent(new window.Event("load")),
        );
        assert.equal(
          avatar.style.getPropertyValue("--account-mark-padding"),
          "8px",
        );
        await React.act(async () =>
          image.dispatchEvent(new window.Event("error")),
        );
        assert.equal(
          avatar.style.getPropertyValue("--account-avatar-size"),
          "40px",
        );
        assert.equal(avatar.querySelector("img"), null);
      }
    }
  });
});

test("the same circular fit protects both identity sizes, retains Mercury detail, and measures each original once", async () => {
  await withDOM(async ({ render, window }) => {
    const reads = mockMarkCanvas(window);
    const { AccountAvatar, CounterpartyAvatar } = await loadComponent(
      "src/components/ui/account-avatar.tsx",
    );
    const view = (base = "https://assets.example", imageMode = false) =>
      React.createElement(
        "div",
        null,
        ...[0, 1, 1, 1, 0].map((institution, i) =>
          React.createElement(i < 3 ? AccountAvatar : CounterpartyAvatar, {
            key: i,
            accountKey: `book:${i}`,
            identityKey: `counterparty:${i}`,
            name: "Operating",
            base,
            institution: {
              ...catalog.institutions[institution],
              presentation: {
                ...catalog.institutions[institution].presentation,
                mode: imageMode ? "image" : "monochrome",
              },
            },
          }),
        ),
      );
    await render(view());
    for (const image of document.querySelectorAll("img"))
      await React.act(async () =>
        image.dispatchEvent(new window.Event("load")),
      );
    assert.equal(
      reads(),
      2,
      "two Wise accounts reuse one original measurement",
    );
    const logos = [...document.querySelectorAll(".institution-logo")];
    const [mercury, wise] = logos.map((el) => Number(el.dataset.circleFit));
    assert.ok(
      mercury > 0.99 && mercury <= 1,
      "round Mercury retains essentially the full24px diameter",
    );
    assert.ok(
      Math.abs(wise - Math.SQRT1_2) < 0.00001,
      "Wise fits its far corner within the same24px circle",
    );
    assert.equal(logos[1].dataset.circleFit, logos[2].dataset.circleFit);
    assert.equal(
      logos[1].dataset.circleFit,
      logos[3].dataset.circleFit,
      "compact Wise reuses the identical original fit",
    );
    const compact = [
      ...document.querySelectorAll("[data-counterparty-avatar]"),
    ];
    for (const avatar of compact) {
      assert.equal(
        avatar.style.getPropertyValue("--account-avatar-size"),
        "24px",
      );
      assert.equal(
        avatar.style.getPropertyValue("--account-mark-padding"),
        "5px",
      );
      assert.ok(
        avatar.querySelector("[data-avatar]").classList.contains("size-6"),
      );
      const logo = avatar.querySelector(".institution-logo");
      const inkRadius =
        Number(logo.dataset.circleFit) *
        Number(logo.dataset.inkRadiusRatio) *
        7;
      assert.ok(
        12 - inkRadius >= 5 - 0.000001,
        "painted corners retain proportional circular clearance at 24px",
      );
    }
    assert.ok(
      logos.every(
        (el) =>
          Number(el.dataset.circleFit) * Number(el.dataset.inkRadiusRatio) <= 1,
      ),
    );
    document.documentElement.className = "dark-mode";
    await render(view());
    assert.equal(
      reads(),
      2,
      "theme changes do not rasterize or replace the original",
    );
    await render(view("https://assets.example", true));
    assert.equal(
      document.querySelectorAll('.institution-logo[data-render="image"]')
        .length,
      5,
    );
    assert.equal(document.querySelectorAll(".institution-logo-mark").length, 0);
    assert.equal(
      reads(),
      2,
      "full-color SVG rendering uses the same cached shape fit",
    );
    assert.equal(
      Number(
        document.querySelectorAll(".institution-logo")[1].dataset.circleFit,
      ),
      wise,
    );
    window.HTMLCanvasElement.prototype.getContext = () => null;
    await render(view("https://unmeasurable.example"));
    for (const image of document.querySelectorAll("img"))
      await React.act(async () =>
        image.dispatchEvent(new window.Event("load")),
      );
    assert.equal(
      document.querySelectorAll(".institution-logo").length,
      0,
      "unmeasurable artwork retains initials, not unchecked geometry",
    );
    assert.equal(document.querySelectorAll("[data-account-avatar]").length, 3);
    assert.equal(
      document.querySelectorAll("[data-counterparty-avatar]").length,
      2,
    );
  });
});
