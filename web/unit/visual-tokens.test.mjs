import assert from "node:assert/strict";
import test from "node:test";

const { count, checkStylesheet, checkSource } = await import("../scripts/check-visual-tokens.mjs");

test("radii come from the scale", () => {
  assert.deepEqual(count(".a { border-radius: var(--radius-lg); } .b { border-radius: 50%; } .c { border-radius: 0; }"), { radius: 0, colour: 0 });
  assert.equal(count(".a { border-radius: 14px; } .b { border-top-left-radius: 3px; }").radius, 2);
  assert.match(checkStylesheet("components/x.css", ".a { border-radius: 10px; }").join(""), /--radius-\*/);
  assert.deepEqual(checkStylesheet("components/x.css", ".a { border-radius: 10px; }", { "components/x.css": { radius: 1, colour: 0 } }), []);
  assert.deepEqual(checkStylesheet("design-system/styles/theme.css", ".a { border-radius: 10px; }"), []);
});

test("colours come from tokens", () => {
  assert.equal(count(".a { color: #fff; background: rgb(0 0 0 / 0.1); }").colour, 2);
  assert.equal(count(":root { --brand: #3366ff; } .a { color: var(--brand); }").colour, 0);
  assert.match(checkStylesheet("components/x.css", ".a { color: oklch(0.5 0 0); }").join(""), /colour token/);
});

test("icons come from one library", () => {
  assert.match(checkSource('import { Pin } from "lucide-react";').join(""), /design-system\/icons/);
  assert.match(checkSource('import { Pin01 } from "@untitledui/icons";').join(""), /design-system\/icons/);
  assert.match(checkSource('import { PushPin } from "@phosphor-icons/react";', "src/components/x.tsx").join(""), /design-system\/icons/);
  assert.deepEqual(checkSource('import { PushPin } from "@phosphor-icons/react";', "src/design-system/icons.tsx"), []);
  assert.deepEqual(checkSource('import { Pin01 } from "../design-system/icons";'), []);
});
