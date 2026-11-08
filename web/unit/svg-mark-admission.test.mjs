import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { validateSVGMark } from "../scripts/lib/svg-mark-admission.mjs";

const svg = (art, attributes = "") =>
  Buffer.from(
    `<svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 32 32" ${attributes}>${art}</svg>`,
  );
const mark = '<path d="M16 0 32 16 16 32 0 16Z" fill="#000"/>';

test("round and square synthetic marks pass the same mark admission profile", async () => {
  const catalog = JSON.parse(
    await readFile(
      new URL("./fixtures/shared-assets/catalog.v1.json", import.meta.url),
      "utf8",
    ),
  );
  for (const item of catalog.institutions.filter((item) =>
    ["mercury", "wise"].includes(item.id),
  )) {
    const bytes = await readFile(
      new URL(
        `./fixtures/shared-assets/sha256/${item.asset.sha256}.svg`,
        import.meta.url,
      ),
    );
    const result = validateSVGMark(bytes);
    assert.deepEqual(result.bounds, [0, 0, 255, 255]);
    assert.ok(result.coverage > 0 && result.coverage < 0.7);
    assert.ok(
      result.circularFit.scale * result.circularFit.radiusRatio <= 1,
      "painted pixels stay inside the24px circle, not just its square bounding box",
    );
    if (item.id === "wise") {
      const oldClearance = 20 - 12 * result.circularFit.radiusRatio;
      assert.ok(
        oldClearance < 3.1,
        "the previous square fit reproduces the corner defect",
      );
      assert.ok(Math.abs(result.circularFit.scale - Math.SQRT1_2) < 0.00001);
    }
  }
});

test("vector clipping definitions and angular corners are not mistaken for badge backgrounds", () => {
  assert.ok(validateSVGMark(svg(mark)));
  assert.ok(
    validateSVGMark(
      Buffer.from(svg(mark).toString().replace('width="32" height="32" ', "")),
    ),
  );
  assert.ok(
    validateSVGMark(
      svg(
        `<defs><clipPath id="crop"><rect width="32" height="32"/></clipPath></defs><g clip-path="url(#crop)">${mark}</g>`,
      ),
    ),
  );
  // A valid triangle touches one complete edge/corners but leaves half transparent.
  assert.ok(validateSVGMark(svg('<path d="M0 0H32L0 32Z"/>')));
});

test("an SVG extension cannot admit embedded raster artwork or an empty opaque canvas", () => {
  for (const canvas of [
    '<rect width="32" height="32" fill="#87ea5c"/>',
    '<rect width="32" height="32" rx="5" fill="#87ea5c"/>',
    '<circle cx="16" cy="16" r="16" fill="#87ea5c"/>',
    '<circle cx="16" cy="16" r="16" fill="#87ea5c" opacity="0.5"/>',
    '<path d="M0 0H32V32H0Z" fill="#fff"/>',
  ])
    if (/rx=|circle/.test(canvas)) {
      const fit = validateSVGMark(svg(canvas + mark)).circularFit;
      assert.ok(fit.scale * fit.radiusRatio <= 1);
    } else
      assert.throws(
        () =>
          validateSVGMark(svg(canvas.replace(/#87ea5c|#fff/g, "#000") + mark)),
        /empty canvas/,
      );
  assert.throws(
    () => validateSVGMark(svg('<image href="data:image/png;base64,AA=="/>')),
    /embedded raster/,
  );
  assert.throws(
    () => validateSVGMark(Buffer.from([137, 80, 78, 71])),
    /encoded data|invalid|UTF-8/i,
  );
});

test("transparent padding and offset use the shared fit; theme-dependent geometry still fails", () => {
  const padded = validateSVGMark(
    Buffer.from(
      svg(mark)
        .toString()
        .replace('viewBox="0 0 32 32"', 'viewBox="-8 -8 48 48"'),
    ),
  );
  assert.ok(padded.circularFit.scale > 1);
  const offset = validateSVGMark(svg('<path d="M16 0 32 8 16 16 0 8Z"/>'));
  assert.ok(offset.circularFit.offsetY > 0);
  assert.ok(offset.circularFit.scale * offset.circularFit.radiusRatio <= 1);
  assert.throws(
    () =>
      validateSVGMark(
        svg(
          `<style>@media (prefers-color-scheme:dark){path{transform:scale(1.2)}}</style>${mark}`,
        ),
      ),
    /colors only/,
  );
  assert.throws(
    () =>
      validateSVGMark(
        svg(
          '<style>@media (prefers-color-scheme:dark){path{fill:#000}}</style><path fill="none" d="M16 0 32 16 16 32 0 16Z"/>',
        ),
      ),
    /visible artwork|silhouette/,
  );
  assert.throws(
    () =>
      validateSVGMark(
        svg(
          `${mark}<style>@media (prefers-color-scheme:dark){#extra{fill:#000}}</style><path id="extra" fill="none" d="M0 0H32V2H0Z"/>`,
        ),
      ),
    /same mark silhouette/,
  );
});

test("background styles and active/external SVG content fail before rendering", () => {
  for (const source of [
    svg(mark, 'style="background:#fff"'),
    svg(`<style>svg{background:#fff}</style>${mark}`),
    svg(`<script>alert(1)</script>${mark}`),
    svg(`<foreignObject/>${mark}`),
    svg(mark, 'onload="alert(1)"'),
    svg(`<path fill="url(https://unrelated.example)" d="M0 0H32V32Z"/>`),
  ])
    assert.throws(
      () => validateSVGMark(source),
      /background|colors only|static vector|active or external|references must be local/,
    );
});
