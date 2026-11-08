import assert from "node:assert/strict";
import test from "node:test";

const { checkSource, checkStylesheet } = await import("../scripts/check-tab-standard.mjs");

test("tab lists come from TabStrip", () => {
  assert.match(checkSource("components/x.tsx", 'import { Tabs, TabList, Tab } from "react-aria-components";').join(""), /TabStrip/);
  assert.match(checkSource("components/x.tsx", '<div role="tablist"><button role="tab">A</button></div>').join(""), /TabStrip/);
  assert.match(checkSource("components/x.tsx", '<TabStrip tabClassName="wide" items={[]} aria-label="X" />').join(""), /shared look/);
  assert.deepEqual(checkSource("components/x.tsx", 'import { Tabs, TabPanel } from "react-aria-components";\n<TabStrip items={[]} aria-label="X" />'), []);
  assert.deepEqual(checkSource("components/ui/tab-strip.tsx", 'import { Tab, TabList } from "react-aria-components";'), []);
});

test("only the tab stylesheet styles the strip", () => {
  assert.match(checkStylesheet("components/x.css", ".page .vult-tab-strip { gap: 32px; }").join(""), /restyles/);
  assert.match(checkStylesheet("components/x.css", ".page .vult-tab-strip-item[data-selected] { color: red; }").join(""), /restyles/);
  assert.match(checkStylesheet("components/x.css", '.page [role="tab"] { padding: 0; }').join(""), /restyles/);
  assert.deepEqual(checkStylesheet("components/x.css", ".page .vult-tab-strip-scroll { margin-bottom: 24px; }"), []);
  assert.deepEqual(checkStylesheet("components/ui/tab-strip.css", ".vult-tab-strip { gap: 0; }"), []);
});
