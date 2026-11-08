// Button standard v1 (docs/design/buttons.md): both Button components share one
// set of colours and sizes, and nothing else is styled as a button.
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const owners = new Set([
  "components/ui/button.tsx",
  "design-system/catalog/components/base/buttons/button.tsx",
]);
const retiredColors = /^(?:link-gray|link-color|primary-destructive|secondary-destructive|tertiary-destructive|link-destructive|default|outline|ghost)$/;

/** Opening tags of a JSX element, with braces and quotes respected. */
export function tags(source, name) {
  const found = [];
  for (const m of source.matchAll(new RegExp(`<${name}\\b`, "g"))) {
    let i = m.index + m[0].length, depth = 0, quote = null;
    for (; i < source.length; i++) {
      const c = source[i];
      if (quote) { if (c === quote) quote = null; }
      else if ("\"'`".includes(c)) quote = c;
      else if (c === "{") depth++;
      else if (c === "}") depth--;
      else if (c === ">" && depth === 0) break;
    }
    found.push(source.slice(m.index, i + 1));
  }
  return found;
}

/** Local names this file gives either Button. */
export function buttonNames(source) {
  const names = [];
  for (const m of source.matchAll(/import\s*\{([^}]*)\}\s*from\s*["']([^"']+)["']/g)) {
    if (!/(?:^|\/)ui\/button$|^\.\/button$|vult-ui\/button$|base\/buttons\/button$/.test(m[2])) continue;
    for (const part of m[1].split(",")) {
      const [name, alias] = part.trim().split(/\s+as\s+/);
      if (name === "Button") names.push(alias || name);
    }
  }
  return names;
}

/** Problems in one component; `file` is relative to src. */
export function checkSource(file, source) {
  if (owners.has(file)) return [];
  const problems = [];
  for (const name of buttonNames(source))
    for (const tag of tags(source, name)) {
      if (/\svariant=/.test(tag)) problems.push(`<${name}> takes color, not variant (docs/design/buttons.md).`);
      for (const [, value] of tag.matchAll(/\scolor="([a-z-]+)"/g))
        if (retiredColors.test(value)) problems.push(`<${name}> color "${value}" is retired; use primary, secondary, tertiary or link.`);
    }
  if (/className=(?:"[^"]*|\{`[^`]*)\bvult-button(?:\s|"|`|-(?:primary|secondary|ghost|link|sm|md|lg)\b)/.test(source))
    problems.push("Only the two Button components use the button classes; use Button instead (docs/design/buttons.md).");
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
  const failures = [];
  for (const file of await sources(src)) {
    const rel = path.relative(src, file).split(path.sep).join("/");
    for (const p of checkSource(rel, await readFile(file, "utf8"))) failures.push(`${rel}: ${p}`);
  }
  if (failures.length) {
    console.error(`Button standard: ${failures.length} problem(s)\n${failures.join("\n")}`);
    process.exit(1);
  }
  console.log("Button standard: PASS");
}
