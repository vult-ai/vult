import { documentEnginePlugin } from "./document-engine-plugin.mjs";
import { phosphorPlugin } from "./phosphor-plugin.mjs";
import { createRequire } from "node:module";
import { build } from "esbuild";
import { JSDOM } from "jsdom";

const require = createRequire(import.meta.url);
export const React = require("react");

export async function loadComponent(path) {
  const bundle = await build({
    entryPoints: [path],
    plugins: [documentEnginePlugin, phosphorPlugin],
    bundle: true,
    write: false,
    platform: "node",
    format: "cjs",
    packages: "external",
    loader: { ".module.css": "empty", ".css": "empty" },
    alias: { "@": new URL("../../src", import.meta.url).pathname },
  });
  const module = { exports: {} };
  new Function("module", "exports", "require", bundle.outputFiles[0].text)(
    module,
    module.exports,
    require,
  );
  return module.exports;
}

export async function withDOM(run, { amountsHidden = false } = {}) {
  const dom = new JSDOM('<div id="root"></div>', {
    url: "http://localhost:3100",
    pretendToBeVisual: true,
  });
  // Tests read the Dashboard's figures; hiding them has its own test.
  if (!amountsHidden) dom.window.localStorage.setItem("vult:amounts-hidden", "shown");
  const saved = new Map();
  const values = {
    window: dom.window,
    document: dom.window.document,
    navigator: dom.window.navigator,
    self: dom.window,
    IS_REACT_ACT_ENVIRONMENT: true,
    requestAnimationFrame: dom.window.requestAnimationFrame.bind(dom.window),
    cancelAnimationFrame: dom.window.cancelAnimationFrame.bind(dom.window),
    getComputedStyle: dom.window.getComputedStyle.bind(dom.window),
    localStorage: dom.window.localStorage,
    matchMedia: (query) => ({
      matches: query.includes("min-width"),
      media: query,
      addEventListener() {},
      removeEventListener() {},
    }),
    ResizeObserver: class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
    CSS: {
      escape: (value) => String(value).replace(/[^a-zA-Z0-9_-]/g, "\\$&"),
    },
  };
  dom.window.matchMedia = values.matchMedia;
  dom.window.ResizeObserver = values.ResizeObserver;
  dom.window.Element.prototype.getAnimations = () => [];
  dom.window.HTMLCanvasElement.prototype.getContext = () => ({
    drawImage() {},
  });
  dom.window.HTMLElement.prototype.scrollTo = function ({ top, left }) {
    if (top !== undefined) this.scrollTop = top;
    if (left !== undefined) this.scrollLeft = left;
  };
  for (const name of [
    "Element",
    "DOMRect",
    "HTMLElement",
    "HTMLInputElement",
    "HTMLButtonElement",
    "HTMLAnchorElement",
    "HTMLSelectElement",
    "HTMLTextAreaElement",
    "HTMLTableElement",
    "HTMLTableRowElement",
    "HTMLTableCellElement",
    "HTMLTableSectionElement",
    "HTMLFormElement",
    "FormData",
    "Node",
    "Event",
    "CustomEvent",
    "MouseEvent",
    "KeyboardEvent",
    "FocusEvent",
    "MutationObserver",
    "NodeFilter",
    "DocumentFragment",
    "ShadowRoot",
    "SVGElement",
  ])
    values[name] = dom.window[name];
  for (const [key, value] of Object.entries(values)) {
    saved.set(key, Object.getOwnPropertyDescriptor(globalThis, key));
    Object.defineProperty(globalThis, key, {
      configurable: true,
      writable: true,
      value,
    });
  }
  const { createRoot } = require("react-dom/client");
  const root = createRoot(document.getElementById("root"));
  let mounted = true;
  const render = async (element) => React.act(async () => root.render(element));
  const unmount = async () => {
    if (mounted) {
      await React.act(async () => root.unmount());
      mounted = false;
    }
  };
  try {
    await run({ render, unmount, window: dom.window });
  } finally {
    await unmount();
    await new Promise((done) => setTimeout(done, 0));
    dom.window.close();
    for (const [key, descriptor] of saved) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
      else delete globalThis[key];
    }
  }
}
