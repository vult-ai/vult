// Search standard v2 (docs/design/search.md): the finder is the one search.
// A collection shows the search it is filtered by (SearchFilter) and has no
// search field of its own.
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

/** The one search control, which draws the field itself. */
export const owners = new Set(["components/shell/book-finder.tsx"]);

/** Problems in one component; `file` is relative to src. */
export function checkSource(file, source) {
  if (owners.has(file) || file.startsWith("design-system/catalog/")) return [];
  const problems = [];
  const line = (i) => source.slice(0, i).split("\n").length;
  for (const m of source.matchAll(/\bSearchField\b/g))
    problems.push(`line ${line(m.index)}: search through the finder (⌘K), not a SearchField of its own (docs/design/search.md).`);
  // A collection toolbar has no search field: it shows the finder's search as a SearchFilter.
  if (/<CollectionToolbar\b|-toolbar\.tsx$/.test(source + file))
    for (const m of source.matchAll(/type=["']search["']/g))
      problems.push(`line ${line(m.index)}: a collection is searched from the finder and shows that search with SearchFilter (docs/design/search.md).`);
  // ⌘K belongs to the finder.
  for (const m of source.matchAll(/(metaKey|ctrlKey)[^\n]{0,80}["']k["']|["']k["'][^\n]{0,80}(metaKey|ctrlKey)/gi))
    problems.push(`line ${line(m.index)}: ⌘K opens the finder; bind it nowhere else (docs/design/search.md).`);
  return problems;
}

async function sources(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  const nested = await Promise.all(entries.map((e) =>
    e.isDirectory() ? sources(path.join(dir, e.name)) : /\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name) ? [path.join(dir, e.name)] : []));
  return nested.flat();
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  const src = path.join(web, "src");
  const failures = [];
  for (const file of await sources(src)) {
    const rel = path.relative(src, file).split(path.sep).join("/");
    for (const p of checkSource(rel, await readFile(file, "utf8"))) failures.push(`${rel}: ${p}`);
  }
  if (failures.length) {
    console.error(`Search standard: ${failures.length} problem(s)\n${failures.join("\n")}`);
    process.exit(1);
  }
  console.log("Search standard: PASS");
}
