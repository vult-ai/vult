import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent } from "./helpers/dom.mjs";

test("lower selectors open above within the dialog while upper selectors use the space below", async () => {
  const { choiceMenuPosition } = await loadComponent(
    "src/components/ui/choice-menu-position.ts",
  );
  const dialog = { top: 133, bottom: 593 },
    viewport = { top: 0, bottom: 726 };
  assert.deepEqual(
    choiceMenuPosition({ top: 459, bottom: 499 }, dialog, viewport),
    { placement: "top start", maxHeight: 256 },
  );
  assert.deepEqual(
    choiceMenuPosition({ top: 245, bottom: 285 }, dialog, viewport),
    { placement: "bottom start", maxHeight: 256 },
  );
});

test("short and partly obscured viewports cap the options to the reachable space", async () => {
  const { choiceMenuPosition } = await loadComponent(
    "src/components/ui/choice-menu-position.ts",
  );
  assert.deepEqual(
    choiceMenuPosition(
      { top: 110, bottom: 150 },
      { top: 16, bottom: 284 },
      { top: 0, bottom: 300 },
    ),
    { placement: "bottom start", maxHeight: 120 },
  );
  assert.deepEqual(
    choiceMenuPosition(
      { top: 180, bottom: 220 },
      { top: 16, bottom: 284 },
      { top: 100, bottom: 250 },
    ),
    { placement: "top start", maxHeight: 66 },
  );
});

test("a list with usable room below opens downward instead of covering its label", async () => {
  const { choiceMenuPosition } = await loadComponent(
    "src/components/ui/choice-menu-position.ts",
  );
  // A field low in a tall dialog: 180px below, 300px above.
  assert.deepEqual(
    choiceMenuPosition({ top: 404, bottom: 444 }, { top: 100, bottom: 638 }, { top: 0, bottom: 900 }),
    { placement: "bottom start", maxHeight: 180 },
  );
});
