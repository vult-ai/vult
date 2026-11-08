import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

const buttons = await import("../scripts/check-button-standard.mjs");
const forms = await import("../scripts/check-form-standard.mjs");

const ui = 'import { Button } from "../ui/button";\n';
const catalog = 'import { Button as ActionButton } from "../../design-system/catalog/components/vult-ui/button";\n';

test("both Buttons take one colour vocabulary", () => {
  assert.match(buttons.checkSource("components/x.tsx", ui + '<Button variant="ghost" size="sm">Retry</Button>').join(""), /color, not variant/);
  assert.match(buttons.checkSource("components/x.tsx", catalog + '<ActionButton color="link-gray">Open</ActionButton>').join(""), /retired/);
  assert.match(buttons.checkSource("components/x.tsx", ui + '<Button\n  color={ok ? "primary" : "ghost"}\n  variant={x}\n>Go</Button>').join(""), /color, not variant/);
  assert.deepEqual(buttons.checkSource("components/x.tsx", ui + catalog + '<Button color="tertiary" size="sm" onClick={() => go()}>Retry</Button><ActionButton color="link" href="/a">Open</ActionButton>'), []);
  // Other components keep their own variant props.
  assert.deepEqual(buttons.checkSource("components/x.tsx", ui + '<Dialog variant="navigation" />'), []);
});

test("nothing else is styled as a button", () => {
  assert.match(buttons.checkSource("components/x.tsx", '<a className="vult-button vult-button-ghost vult-button-sm" href="/">Sign in</a>').join(""), /Only the two Button/);
  assert.match(buttons.checkSource("components/x.tsx", "<a className={`vult-button ${x}`}>Go</a>").join(""), /Only the two Button/);
  assert.deepEqual(buttons.checkSource("components/x.tsx", '<span className="vult-button-copy-note">x</span>'), []);
  assert.deepEqual(buttons.checkSource("components/ui/button.tsx", '<button className="vult-button">'), []);
});

test("native controls sit inside the shared field", () => {
  assert.match(forms.checkSource("components/x.tsx", '<div><input name="code" /></div>').join(""), /wrap this <input>/);
  assert.match(forms.checkSource("components/x.tsx", "<select value={v}><option /></select>").join(""), /wrap this <select>/);
  assert.deepEqual(forms.checkSource("components/x.tsx", '<Field label="GL code"><input name="code" /></Field>'), []);
  assert.deepEqual(forms.checkSource("components/x.tsx", '<SettingsRow label="Name"><NativeFieldControl><input name="name" /></NativeFieldControl></SettingsRow>'), []);
  assert.deepEqual(forms.checkSource("components/x.tsx", '<label><input type="checkbox" /> Remember</label>'), []);
  assert.deepEqual(forms.checkSource("components/form.tsx", "<select />"), []);
});

test("the documents state the sizes the code uses", async () => {
  const doc = await readFile(new URL("../../docs/design/buttons.md", import.meta.url), "utf8");
  for (const value of ["32px", "36px", "40px", "primary", "secondary", "tertiary", "link", "destructive"])
    assert.match(doc, new RegExp(value));
  const css = await readFile(new URL("../src/design-system/styles/vult-controls.css", import.meta.url), "utf8");
  for (const [size, px] of [["sm", 32], ["md", 36], ["lg", 40]])
    assert.match(css, new RegExp(`--vult-control-height-${size}:\\s*${px}px`));
});
