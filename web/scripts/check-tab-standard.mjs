// Tab standard v1 (docs/design/tabs.md): every tab list is the shared TabStrip,
// and only its stylesheet styles the strip and its tabs.
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const component = "components/ui/tab-strip.tsx";
const stylesheet = "components/ui/tab-strip.css";

/** Problems in one component; `file` is relative to src. */
export function checkSource(file, source) {
  if (file === component || file.startsWith("design-system/catalog/")) return [];
  const problems = [];
  const aria = /import\s*\{([^}]*)\}\s*from\s*["']react-aria-components["']/.exec(source)?.[1] ?? "";
  if (/\b(?:TabList|Tab)\b(?!Panel)/.test(aria.replace(/\bTabPanel\b/g, "")) || /\srole=["']tab(?:list)?["']/.test(source))
    problems.push("Build tabs with TabStrip, not by hand (docs/design/tabs.md).");
  if (/\stabClassName=/.test(source))
    problems.push("Tabs keep the shared look; don't pass classes to individual tabs (docs/design/tabs.md).");
  return problems;
}

/** Problems in one stylesheet; `file` is relative to src. */
export function checkStylesheet(file, css) {
  if (file === stylesheet) return [];
  const text = css.replace(/\/\*[\s\S]*?\*\//g, "");
  const problems = [];
  for (const m of text.matchAll(/([^{}]+)\{/g)) {
    const selector = m[1].trim();
    if (selector.startsWith("@")) continue;
    if (/\.vult-tab-strip(?![\w-])|\.vult-tab-strip-item\b|\[role=["']?tab(?:list)?["']?\]/.test(selector))
      problems.push(`"${selector.replace(/\s+/g, " ")}" restyles the tab strip; only placement of .vult-tab-strip-scroll is allowed (docs/design/tabs.md).`);
  }
  return problems;
}

async function files(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  const nested = await Promise.all(entries.map((e) =>
    e.isDirectory() ? files(path.join(dir, e.name)) : /\.(?:tsx|css)$/.test(e.name) ? [path.join(dir, e.name)] : []));
  return nested.flat();
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  const src = path.join(web, "src");
  const failures = [];
  for (const file of await files(src)) {
    const rel = path.relative(src, file).split(path.sep).join("/");
    const text = await readFile(file, "utf8");
    for (const p of rel.endsWith(".css") ? checkStylesheet(rel, text) : checkSource(rel, text)) failures.push(`${rel}: ${p}`);
  }
  if (failures.length) {
    console.error(`Tab standard: ${failures.length} problem(s)\n${failures.join("\n")}`);
    process.exit(1);
  }
  console.log("Tab standard: PASS");
}
