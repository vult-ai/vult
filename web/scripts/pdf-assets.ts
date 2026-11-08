import { createRequire } from "node:module";
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import type { Plugin } from "vite";

const require = createRequire(import.meta.url);
const root = dirname(require.resolve("pdfjs-dist/package.json"));
const version = JSON.parse(
  readFileSync(join(root, "package.json"), "utf8"),
).version;
export const pdfAssetBase = `/assets/pdfjs/${version}/`;

/** Package-versioned, same-origin PDF runtime assets in dev and the Go bundle. */
export function pdfAssets(): Plugin {
  const files = new Map<string, string>([
    ["pdf.worker.min.mjs", join(root, "build/pdf.worker.min.mjs")],
    ["LICENSE", join(root, "LICENSE")],
  ]);
  for (const folder of ["cmaps", "standard_fonts", "wasm", "iccs"])
    for (const entry of readdirSync(join(root, folder), {
      withFileTypes: true,
    }))
      if (entry.isFile())
        files.set(`${folder}/${entry.name}`, join(root, folder, entry.name));
  return {
    name: "vult-pdf-runtime-assets",
    configureServer(server) {
      server.middlewares.use(pdfAssetBase, (request, response) => {
        const path = new URL(
          request.url || "/",
          "http://local.invalid",
        ).pathname.slice(1);
        const file = files.get(path);
        if (!file || !["GET", "HEAD"].includes(request.method || "")) {
          response.statusCode = 404;
          response.end();
          return;
        }
        response.setHeader(
          "Content-Type",
          path.endsWith(".mjs")
            ? "text/javascript"
            : path.endsWith(".wasm")
              ? "application/wasm"
              : "application/octet-stream",
        );
        response.setHeader(
          "Cache-Control",
          "public, max-age=31536000, immutable",
        );
        response.setHeader("X-Content-Type-Options", "nosniff");
        response.end(
          request.method === "HEAD" ? undefined : readFileSync(file),
        );
      });
    },
    generateBundle() {
      for (const [name, file] of files)
        this.emitFile({
          type: "asset",
          fileName: `${pdfAssetBase.slice(1)}${name}`,
          source: readFileSync(file),
        });
    },
  };
}
