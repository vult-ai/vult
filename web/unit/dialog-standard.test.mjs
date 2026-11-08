import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

const { checkStylesheet, checkSource, checkTokens, dialogClasses, tokens } = await import("../scripts/check-dialog-standard.mjs");

test("screens cannot size a dialog", () => {
  for (const css of [
    ".dialog-content.agent-dialog:not(.dialog-navigation) { width: min(720px, calc(100vw - 32px)); }",
    ".dialog-content.picker { height: min(480px, 90dvh); }",
    "@media (width < 768px) { .dialog-content.picker { max-height: 100dvh; } }",
    ".agent-dialog { width: 720px; }",
  ])
    assert.match(checkStylesheet("components/agent.css", css, ["agent-dialog"]).join("\n"), /sizes a dialog/, css);
  assert.deepEqual(checkStylesheet("components/agent.css", ".agent-dialog .dialog-body { padding: 0; max-height: 40vh; }", ["agent-dialog"]), []);
  assert.deepEqual(checkStylesheet("components/agent.css", ".agent-dialog { padding: 0; }", ["agent-dialog"]), []);
  assert.deepEqual(checkStylesheet("components/ui/controls.css", ".dialog-content { width: 640px; }"), []);
});

test("the classes a screen passes to Dialog are the ones checked", () => {
  const source = 'import { Dialog } from "../ui/dialog";\n<Dialog open title="GL code"\n  size="small"\n  className="account-code-dialog">';
  assert.deepEqual(dialogClasses(source), ["account-code-dialog"]);
  assert.deepEqual(dialogClasses('import { Dialog } from "react-aria-components";\n<Dialog className="statement-date-dialog">'), []);
});

test("modals come from the shared Dialog", () => {
  assert.match(checkSource("components/x.tsx", 'import * as D from "@radix-ui/react-dialog";').join(""), /shared Dialog/);
  assert.match(checkSource("components/x.tsx", '<div role="dialog" aria-modal="true">').join(""), /shared Dialog/);
  assert.match(checkSource("components/x.tsx", "<ModalOverlay><Modal>").join(""), /shared Dialog/);
  assert.deepEqual(checkSource("components/ui/dialog.tsx", 'import * as D from "@radix-ui/react-dialog";'), []);
  assert.deepEqual(checkSource("components/x.tsx", "document.querySelector('[role=\"dialog\"]')"), []);
});

test("the dialog tokens keep the documented values", async () => {
  const css = await readFile(new URL("../src/components/ui/controls.css", import.meta.url), "utf8");
  assert.deepEqual(checkTokens(css), []);
  assert.match(checkTokens(css.replace("--vult-dialog-width-medium: 640px", "--vult-dialog-width-medium: 600px")).join(""), /640px/);
  const doc = await readFile(new URL("../../docs/design/dialogs.md", import.meta.url), "utf8");
  for (const value of ["480px", "640px", "800px", "1280px", "16px", "8px", "768px"])
    assert.match(doc, new RegExp(value), `the document states ${value}`);
  assert.equal(Object.keys(tokens).length, 6);
});
