import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import test from "node:test";
import { build } from "esbuild";
import { pdfAssets, pdfAssetBase } from "../scripts/pdf-assets.ts";

const require = createRequire(import.meta.url);
const fixtureName = "__pdfEngineFixture";
let engineImport = 0;

async function loadEngine() {
  const result = await build({
    entryPoints: [new URL("../src/lib/document-engine.ts", import.meta.url).pathname],
    bundle: true, format: "esm", platform: "browser", write: false,
    define: { __VULT_PDF_ASSET_BASE__: JSON.stringify(pdfAssetBase) },
    plugins: [{ name: "pdf-library-fixture", setup(builder) {
      builder.onResolve({ filter: /^pdfjs-dist\/legacy\/build\/pdf\.mjs$/ }, () => ({ path: "pdf-library", namespace: "fixture" }));
      builder.onLoad({ filter: /.*/, namespace: "fixture" }, () => ({ contents: `
        const fixture = globalThis.${fixtureName};
        export const GlobalWorkerOptions = fixture.workerOptions;
        export function getDocument() { fixture.loads++; return fixture.task; }
      ` }));
    } }],
  });
  return import(`data:text/javascript;base64,${Buffer.from(result.outputFiles[0].contents).toString("base64")}#${++engineImport}`);
}

test("legacy PDF workers have a distinct immutable URL in development and builds", async () => {
  const expected = await readFile(require.resolve("pdfjs-dist/legacy/build/pdf.worker.min.mjs"));
  assert.match(pdfAssetBase, /\/legacy\/$/);
  const plugin = pdfAssets();
  let route, serve;
  plugin.configureServer({ middlewares: { use(path, handler) { route = path; serve = handler; } } });
  assert.equal(route, pdfAssetBase);
  const headers = {};
  let served;
  serve({ url: "/pdf.worker.min.mjs", method: "GET" }, {
    setHeader(name, value) { headers[name] = value; },
    end(value) { served = value; },
  });
  assert.equal(headers["Cache-Control"], "public, max-age=31536000, immutable");
  assert.deepEqual(served, expected);
  let emitted;
  plugin.generateBundle.call({ emitFile(asset) {
    if (asset.fileName === `${pdfAssetBase.slice(1)}pdf.worker.min.mjs`) emitted = asset.source;
  } });
  assert.deepEqual(emitted, expected);
});

test("a PDF page still paints when its text cannot be read, and destroying it releases its abort listener", async () => {
  let painted = 0, destroyed = 0;
  const workerOptions = {};
  const page = {
    getViewport({ scale }) { return { width: 600 * scale, height: 800 * scale }; },
    getTextContent() { return Promise.reject(new Error("Text extraction unavailable")); },
    render() { painted++; return { promise: Promise.resolve(), cancel() {} }; },
  };
  globalThis[fixtureName] = {
    workerOptions, loads: 0,
    task: { promise: Promise.resolve({ numPages: 1, getPage: async () => page }), destroy: async () => { destroyed++; } },
  };
  const previousRatio = Object.getOwnPropertyDescriptor(globalThis, "devicePixelRatio");
  Object.defineProperty(globalThis, "devicePixelRatio", { value: 1, configurable: true });
  const controller = new AbortController();
  let model;
  try {
    const { openDocument } = await loadEngine();
    model = await openDocument(new Blob(["%PDF-1.7 synthetic"]), "pdf", controller.signal);
    assert.equal(workerOptions.workerSrc, `${pdfAssetBase}pdf.worker.min.mjs`);
    const sheet = await model.page(1);
    assert.equal(sheet.text, "");
    assert.equal(sheet.width, 600);
    assert.equal(sheet.height, 800);
    await sheet.paint({}, 1, controller.signal);
    assert.equal(painted, 1);
    model.destroy();
    model = undefined;
    controller.abort();
    assert.equal(destroyed, 1, "destroying the document removes its abort listener");
  } finally {
    model?.destroy();
    controller.abort();
    delete globalThis[fixtureName];
    if (previousRatio) Object.defineProperty(globalThis, "devicePixelRatio", previousRatio);
    else delete globalThis.devicePixelRatio;
  }
});

test("closing during a PDF byte read never starts the decoder after cancellation", async () => {
  const fixture = { workerOptions: {}, loads: 0 };
  globalThis[fixtureName] = fixture;
  const controller = new AbortController();
  let finishBytes, readingBytes;
  const started = new Promise((resolve) => { readingBytes = resolve; });
  const bytes = new Promise((resolve) => { finishBytes = resolve; });
  try {
    const { openDocument } = await loadEngine();
    const loading = openDocument({ arrayBuffer() { readingBytes(); return bytes; } }, "pdf", controller.signal);
    await started;
    controller.abort();
    finishBytes(new ArrayBuffer(0));
    await assert.rejects(loading, { name: "AbortError" });
    assert.equal(fixture.loads, 0);
  } finally {
    controller.abort();
    delete globalThis[fixtureName];
  }
});
