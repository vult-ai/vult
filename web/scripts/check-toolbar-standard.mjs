// Toolbar standard v1 (docs/design/toolbars.md): a records table's toolbar is
// a CollectionToolbar, so every collection wraps instead of cutting anything off.
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

/** Problems for one file, given every source file's text by relative path. */
export function checkToolbars(file, source, files) {
  const problems = [];
  for (const m of source.matchAll(/\btoolbar=\{\s*(<\s*([A-Za-z]\w*)|[A-Za-z_]\w*)/g)) {
    const component = m[2];
    if (!component) continue; // A variable built elsewhere in this file is checked where it is built.
    if (component === "CollectionToolbar") continue;
    // A named toolbar component must render the shared toolbar itself.
    const definition = Object.entries(files).find(([, text]) => new RegExp(`export function ${component}\\b`).test(text));
    if (!definition || !/<CollectionToolbar\b/.test(definition[1]))
      problems.push(`toolbar <${component}> must render CollectionToolbar.`);
  }
  return problems;
}

async function sources(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  const nested = await Promise.all(entries.map((e) =>
    e.isDirectory() ? sources(path.join(dir, e.name)) : /\.tsx$/.test(e.name) ? [path.join(dir, e.name)] : []));
  return nested.flat();
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  const src = path.join(web, "src");
  const files = {};
  for (const file of await sources(src)) files[path.relative(src, file).split(path.sep).join("/")] = await readFile(file, "utf8");
  const failures = Object.entries(files).flatMap(([file, text]) => checkToolbars(file, text, files).map((p) => `${file}: ${p}`));
  if (failures.length) {
    console.error(`Toolbar standard: ${failures.length} problem(s)\n${failures.join("\n")}`);
    process.exit(1);
  }
  console.log("Toolbar standard: PASS");
}
