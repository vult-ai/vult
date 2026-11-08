import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { build } from "esbuild";
import { phosphorPlugin } from "./phosphor-plugin.mjs";
import { documentEnginePlugin } from "./document-engine-plugin.mjs";

/** Exercise the real host components without mounting the production entrypoint. */
export async function loadApplication({ workspace = "transactions" } = {}) {
  // These regressions explicitly exercise the retained transaction workspace.
  // Product startup defaults to Dashboard when no route is supplied.
  if (window.location.pathname === "/" && !window.location.hash)
    window.history.replaceState(null, "", `/${workspace}`);
  const source = await readFile("src/main.tsx", "utf8");
  const contents =
    source.slice(0, source.lastIndexOf("export function mount(")) +
    '\nexport { App, BookWorkspace, EntryDetail, PersonalSettingsHost }; export { Theme } from "./design-system/theme"; export { rememberDashboardSelection, rememberedDashboardSelection } from "./lib/dashboard-location";';
  const bundle = await build({
    plugins: [documentEnginePlugin, phosphorPlugin],
    stdin: {
      contents,
      resolveDir: new URL("../../src", import.meta.url).pathname,
      loader: "tsx",
    },
    jsx: "automatic",
    bundle: true,
    write: false,
    platform: "node",
    format: "cjs",
    packages: "external",
    loader: { ".module.css": "empty", ".css": "empty" },
  });
  const module = { exports: {} };
  new Function("module", "exports", "require", bundle.outputFiles[0].text)(
    module,
    module.exports,
    createRequire(import.meta.url),
  );
  return module.exports;
}
