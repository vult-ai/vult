// Side panel standard v1 (docs/design/side-panels.md): every panel beside a
// collection uses the one width token, and no stylesheet restates the width.
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const owner = "design-system/collection-inspector.css";
export const width = "304px";

/** Problems in one stylesheet; `file` is relative to src. */
export function checkStylesheet(file, css) {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, "");
  if (file === owner) {
    const m = /--vult-inspector-width:\s*([^;]+);/.exec(text);
    if (!m) return ["Missing --vult-inspector-width."];
    return m[1].trim() === width ? [] : [`--vult-inspector-width is ${m[1].trim()}; the standard says ${width}.`];
  }
  const problems = [];
  if (new RegExp(`(?<![\\d.])${width.replace("px", "")}px\\b`).test(text))
    problems.push(`A side panel is var(--vult-inspector-width), not ${width} (docs/design/side-panels.md).`);
  for (const m of text.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const subjects = m[1].split(",").map((part) => part.trim().split(/\s+/).at(-1));
    if (!subjects.some((subject) => /\.collection-inspector-panel(?![\w-])/.test(subject))) continue;
    // A real width; flex fixes such as min-width: 0 and fluid values are fine.
    const sized = [...m[2].matchAll(/(?:^|[;\s])(?:min-|max-)?width\s*:\s*([^;]+)/g)]
      .some(([, value]) => !/^(?:0|100%|auto|none)$/.test(value.trim()));
    if (sized) problems.push(`"${m[1].trim()}" sizes a side panel; the width comes from the shared token.`);
  }
  return problems;
}

async function files(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  const nested = await Promise.all(entries.map((e) =>
    e.isDirectory() ? files(path.join(dir, e.name)) : /\.css$/.test(e.name) ? [path.join(dir, e.name)] : []));
  return nested.flat();
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  const src = path.join(web, "src");
  const failures = [];
  for (const file of await files(src)) {
    const rel = path.relative(src, file).split(path.sep).join("/");
    for (const p of checkStylesheet(rel, await readFile(file, "utf8"))) failures.push(`${rel}: ${p}`);
  }
  if (failures.length) {
    console.error(`Side panel standard: ${failures.length} problem(s)\n${failures.join("\n")}`);
    process.exit(1);
  }
  console.log("Side panel standard: PASS");
}
