import { pdfAssets, pdfAssetBase } from "./scripts/pdf-assets";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
const throughGo = process.env.VULT_DEV_PROXY === "1";
const port = Number(process.env.VULT_DEV_PORT || 5173);
// Public institution and brand artwork, as in production. "off" shows initials.
const configuredArtwork = process.env.VULT_SHARED_ASSET_ORIGIN ?? "https://assets.vult.ai";
const sharedAssetOrigin = configuredArtwork === "off" ? "" : configuredArtwork;
if (sharedAssetOrigin) {
  const url = new URL(sharedAssetOrigin);
  if (
    url.protocol !== "https:" ||
    !/^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/.test(
      url.hostname,
    ) ||
    url.origin !== sharedAssetOrigin ||
    url.username ||
    url.password ||
    ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname)
  )
    throw new Error(
      "VULT_SHARED_ASSET_ORIGIN must be one canonical public HTTPS origin",
    );
}

export default defineConfig(() => ({
  define: {
    __VULT_PDF_ASSET_BASE__: JSON.stringify(pdfAssetBase),
    // Help lives on the website in every build. Set VULT_DOCS_ORIGIN only to
    // preview documentation you are writing.
    __VULT_DOCUMENTATION_ORIGIN__: JSON.stringify(
      process.env.VULT_DOCS_ORIGIN ?? "https://vult.ai",
    ),
  },
  html: throughGo ? { cspNonce: "__VULT_DEV_NONCE__" } : undefined,
  resolve: {
    alias: {
      "@vult": fileURLToPath(
        new URL("./src/design-system/catalog", import.meta.url),
      ),
    },
  },
  plugins: [
    pdfAssets(),
    {
      // Institution and brand artwork is read from the shared asset origin in
      // development too; no copy of it is kept in this repository.
      name: "vult-shared-asset-origin",
      // Builds keep the placeholder: the server fills in its configured origin.
      apply: "serve",
      transformIndexHtml(html) {
        return html.replace("__VULT_SHARED_ASSET_ORIGIN__", sharedAssetOrigin);
      },
    },
    {
      // Sonner adds its stylesheet as an inline <style> element, which the
      // app's content security policy refuses. Its own stylesheet file is
      // bundled instead (src/components/ui/toaster.css).
      name: "vult-sonner-styles",
      enforce: "pre",
      transform(code, id) {
        if (!/node_modules\/sonner\/dist\/index\.m?js$/.test(id)) return;
        return code.replace(/^__insertCSS\(".*"\);?$/m, "");
      },
    },
    react(),
    tailwindcss(),
    {
      name: "vult-dev-launcher",
      apply: "serve",
      configureServer(server) {
        server.middlewares.use("/__vult_dev", (_request, response) => {
          response.setHeader("Content-Type", "application/json");
          response.setHeader("Cache-Control", "no-store");
          response.end(
            JSON.stringify({ session: process.env.VULT_DEV_SESSION }),
          );
        });
      },
    },
  ],
  server: {
    host: "127.0.0.1",
    port,
    strictPort: true,
    allowedHosts: ["host.docker.internal"],
    ws: throughGo
      ? { host: "127.0.0.1", clientPort: 8080, path: "/__vult_hmr" }
      : undefined,
    proxy: throughGo
      ? undefined
      : { "/api": "http://127.0.0.1:8080", "/auth": "http://127.0.0.1:8080" },
    warmup: { clientFiles: ["./src/bootstrap.ts", "./src/main.tsx"] },
  },
  build: {
    sourcemap: false,
    rolldownOptions: {
      output: {
        chunkFileNames: "assets/chunk-[hash].js",
        assetFileNames: (asset) =>
          asset.names.some((name) => name.endsWith(".css"))
            ? "assets/style-[hash][extname]"
            : "assets/[name]-[hash][extname]",
      },
    },
  },
}));
