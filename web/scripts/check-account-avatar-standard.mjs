import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { JSDOM } from "jsdom";
import {
  ACCOUNT_AVATAR_STANDARD,
  COUNTERPARTY_AVATAR_STANDARD,
  COUNTERPARTY_FALLBACK_VERSION,
} from "../src/design-system/account-avatar-standard.ts";

assert.equal(COUNTERPARTY_FALLBACK_VERSION, 2);

// This is the declared v2 geometry contract, not an adaptable snapshot. Changing
// it requires a new version of docs/design/avatars.md.
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
assert.equal(
  ACCOUNT_AVATAR_STANDARD.diameter - 2 * ACCOUNT_AVATAR_STANDARD.padding,
  ACCOUNT_AVATAR_STANDARD.markSize,
);
// The compact role shares the clearance proportion without changing account v2.
assert.deepEqual(COUNTERPARTY_AVATAR_STANDARD, {
  version: 1,
  sourceSize: "xs",
  diameter: 24,
  markSize: 14,
  padding: 5,
  markShape: "circle",
  fit: "painted-radius",
  measurementSize: 512,
});
assert.ok(
  COUNTERPARTY_AVATAR_STANDARD.padding /
    COUNTERPARTY_AVATAR_STANDARD.diameter >=
    ACCOUNT_AVATAR_STANDARD.padding / ACCOUNT_AVATAR_STANDARD.diameter,
);
const sourceRoot = fileURLToPath(new URL("../src/", import.meta.url));
const owner = path.join(sourceRoot, "components/ui/account-avatar.css");
const componentOwner = path.join(
  sourceRoot,
  "components/ui/account-avatar.tsx",
);
async function checkOwners(folder) {
  for (const entry of await readdir(folder, { withFileTypes: true })) {
    const file = path.join(folder, entry.name);
    if (entry.isDirectory()) await checkOwners(file);
    else if (
      /\.tsx?$/.test(file) &&
      file !== componentOwner &&
      /from\s+["'][^"']*\/institution-logo["']/.test(
        await readFile(file, "utf8"),
      )
    )
      throw Error(
        "Use AccountAvatar or CounterpartyAvatar; the institution image renderer is internal to the shared standard",
      );
    else if (
      file.endsWith(".css") &&
      file !== owner &&
      /\.(?:account-avatar|institution-logo)\b|\[data-(?:account-avatar|counterparty-avatar|avatar-standard)\b/.test(
        await readFile(file, "utf8"),
      )
    )
      throw Error(
        `Account icon styling has one owner; remove the override in ${path.relative(sourceRoot, file)}`,
      );
  }
}
await checkOwners(sourceRoot);
const css = await readFile(owner, "utf8");
const dom = new JSDOM("<style></style>");
try {
  dom.window.document.querySelector("style").textContent = css;
  const rules = [...dom.window.document.styleSheets[0].cssRules];
  assert.ok(
    rules.every((rule) => rule.type === 1),
    "Account icon geometry cannot have responsive or conditional rule blocks",
  );
  const expected = {
    ".account-avatar": {
      width: "var(--account-avatar-size)",
      height: "var(--account-avatar-size)",
    },
    ".institution-logo": {
      padding: "var(--account-mark-padding)",
      "border-radius": "50%",
    },
    ".institution-logo-viewport": {
      inset: "var(--account-mark-padding)",
      "border-radius": "50%",
      overflow: "hidden",
    },
    ".institution-logo-mark": {
      inset: "0",
      transform: "scale(var(--institution-circle-fit)) translate(var(--institution-center-x), var(--institution-center-y))",
      "transform-origin": "center",
    },
    ".institution-logo-viewport > img": {
      transform: "scale(var(--institution-circle-fit)) translate(var(--institution-center-x), var(--institution-center-y))",
      "transform-origin": "center",
    },
  };
  for (const [selector, declarations] of Object.entries(expected)) {
    const matches = rules.filter((rule) => rule.selectorText === selector);
    assert.equal(
      matches.length,
      1,
      `${selector} must have one declaration owner`,
    );
    for (const [property, value] of Object.entries(declarations))
      assert.equal(
        matches[0].style.getPropertyValue(property),
        value,
        `${selector}: ${property} must follow the shared contract`,
      );
  }
  for (const rule of rules) {
    if (/dark-mode|light-mode|data-tone/.test(rule.selectorText))
      for (const property of Array.from(rule.style))
        assert.ok(
          [
            "background",
            "background-color",
            "color",
            "--account-avatar-color",
          ].includes(property),
          "Brand/theme selectors may change color only",
        );
  }
} finally {
  dom.window.close();
}
console.log(
  "Account avatar v2: pinned md 40px circle / 24px circular safe area / 8px radial clearance; one painted-radius fit, no brand/theme/viewport overrides.",
);
console.log(
  "Counterparty avatar v1: pinned xs 24px circle / 14px circular safe area / 5px radial clearance; shared renderer and original fit.",
);
