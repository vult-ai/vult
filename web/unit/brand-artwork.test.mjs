import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { Resvg } from "@resvg/resvg-js";
import { validateBrandArtwork } from "../scripts/lib/brand-artwork-admission.mjs";
import { circularMarkFit } from "../src/lib/circular-mark-fit.ts";
import { institutionAssetKey } from "../src/lib/shared-institutions.ts";

test("every fixture original uses an admitted format and preserves radial clearance", async () => {
  const catalog = JSON.parse(
    await readFile(
      new URL("./fixtures/shared-assets/brand-logos.v1.json", import.meta.url),
    ),
  );
  for (const item of catalog.institutions) {
    const bytes = await readFile(
      new URL(
        `./fixtures/shared-assets/${institutionAssetKey(item)}`,
        import.meta.url,
      ),
    );
    const metrics = await validateBrandArtwork(bytes, item.asset.media_type);
    assert.ok(
      metrics.circularFit.scale * metrics.circularFit.radiusRatio <= 1,
      item.id,
    );
  }
});
test("official raster originals and padded wordmarks use the same visible-ink fit", async () => {
  const source =
    '<svg xmlns="http://www.w3.org/2000/svg" width="96" height="32" viewBox="0 0 96 32"><path d="M24 8h8v16h-8zM40 8h8v16h-8zM56 8h8v16h-8z"/></svg>';
  const png = new Resvg(source).render().asPng();
  const result = await validateBrandArtwork(png, "image/png");
  assert.ok(result.circularFit.scale > 1);
  assert.ok(result.circularFit.scale * result.circularFit.radiusRatio <= 1);
  await assert.rejects(
    () => validateBrandArtwork(png, "image/webp"),
    /Invalid artwork/,
  );
  await assert.rejects(
    () => validateBrandArtwork(png, "image/jpeg"),
    /Unsupported/,
  );
});
test("off-center transparent artwork is centered without changing the file or geometry", () => {
  const pixels = new Uint8Array(100 * 100 * 4);
  for (let y = 10; y < 30; y++)
    for (let x = 20; x < 40; x++) pixels[(y * 100 + x) * 4 + 3] = 255;
  const before = pixels.slice();
  const fit = circularMarkFit(pixels, 100, 100);
  assert.equal(fit.offsetX, 20);
  assert.equal(fit.offsetY, 30);
  assert.ok(fit.scale > 1);
  assert.ok(fit.scale * fit.radiusRatio <= 1);
  assert.deepEqual(pixels, before);
});
test("XML prologs/comments remain unchanged and active SVG content is rejected", async () => {
  const xml = Buffer.from(
    '<?xml version="1.0"?><!-- official original --><svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 32 32"><path d="M16 0 32 16 16 32 0 16Z"/></svg>',
  );
  assert.ok(await validateBrandArtwork(xml, "image/svg+xml"));
  for (const art of [
    "<script>alert(1)</script>",
    '<image href="https://other.example/private"/>',
    '<path onclick="alert(1)"/>',
  ])
    await assert.rejects(() =>
      validateBrandArtwork(
        Buffer.from(
          `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">${art}</svg>`,
        ),
        "image/svg+xml",
      ),
    );
  await assert.rejects(
    () =>
      validateBrandArtwork(
        Buffer.from(
          '<!DOCTYPE svg [<!ENTITY x "oops">]><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"/>',
        ),
        "image/svg+xml",
      ),
    /static/,
  );
});
