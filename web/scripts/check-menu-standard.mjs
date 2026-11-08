// Menu standard v1 (docs/design/menus.md): menus size from their content within
// shared limits. Screens may not set a menu's width or build a menu by hand,
// and the shared tokens must keep the documented values.
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const shared = [
  "design-system/catalog/components/base/dropdown/",
  "design-system/catalog/components/base/select/",
  "design-system/catalog/components/vult-ui/dropdown.tsx",
];
const width = /(^|[\s"'`])(?:min-|max-)?w-(?:\d|\[|\(|full|screen|max|min|fit|auto)/;

export const tokens = {
  "--vult-menu-min-width": "144px",
  "--vult-menu-max-width": "280px",
  "--vult-menu-select-max-width": "320px",
  "--vult-menu-max-height": "360px",
  "--vult-menu-padding": "4px",
  "--vult-menu-row-height": "var(--vult-control-height-sm)",
  "--vult-menu-row-start": "8px",
  "--vult-menu-row-end": "12px",
  "--vult-menu-icon-size": "var(--vult-control-icon-size)",
  "--vult-menu-icon-gap": "8px",
};

/** Problems in one source file; `file` is relative to src. */
export function checkSource(file, source) {
  const problems = [];
  if (shared.some((prefix) => file.startsWith(prefix))) return problems;
  for (const match of source.matchAll(/<(?:Dropdown\.Popover|Popover)\b[^>]*>/gs)) {
    const classes = [...match[0].matchAll(/className=(?:"([^"]*)"|\{[^}]*?["'`]([^"'`]*)["'`])/g)].map((m) => m[1] ?? m[2]).join(" ");
    if (width.test(" " + classes))
      problems.push("A menu's width follows its content within the shared limits; remove the width class (docs/design/menus.md).");
  }
  // Popover settings kept in an object and spread onto the popover.
  for (const m of source.matchAll(/placement:[^;]{0,240}?className:\s*["'`]([^"'`]*)["'`]/gs))
    if (width.test(" " + m[1]))
      problems.push("A menu's width follows its content within the shared limits; remove the width class (docs/design/menus.md).");
  if (/popoverClassName=(?:"[^"]*"|\{[^}]*\})/.test(source)) {
    for (const m of source.matchAll(/popoverClassName=(?:"([^"]*)"|\{[^}]*?["'`]([^"'`]*)["'`])/g))
      if (width.test(" " + (m[1] ?? m[2]))) problems.push("A select list's width follows the menu standard; remove the width from popoverClassName.");
  }
  // A JSX role attribute, not a selector string such as '[role="listbox"]'.
  if (/\srole=["'](?:menu|listbox|menuitem|option)["']/.test(source))
    problems.push("Build menus and option lists from the shared Dropdown or Select components, not by hand.");
  return problems;
}

/** Problems in the shared token block. */
export function checkTokens(css) {
  const problems = [];
  for (const [name, value] of Object.entries(tokens)) {
    const m = new RegExp(`${name}:\\s*([^;]+);`).exec(css);
    if (!m) problems.push(`Missing ${name}.`);
    else if (m[1].trim() !== value) problems.push(`${name} is ${m[1].trim()}; the standard says ${value}.`);
  }
  return problems;
}

async function sources(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  const nested = await Promise.all(entries.map((e) =>
    e.isDirectory() ? sources(path.join(dir, e.name)) : /\.tsx?$/.test(e.name) ? [path.join(dir, e.name)] : []));
  return nested.flat();
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  const src = path.join(web, "src");
  const failures = [];
  for (const file of await sources(src)) {
    const rel = path.relative(src, file).split(path.sep).join("/");
    for (const p of checkSource(rel, await readFile(file, "utf8"))) failures.push(`${rel}: ${p}`);
  }
  for (const p of checkTokens(await readFile(path.join(src, "design-system/styles/vult-controls.css"), "utf8")))
    failures.push(`vult-controls.css: ${p}`);
  if (failures.length) {
    console.error(`Menu standard: ${failures.length} problem(s)\n${failures.join("\n")}`);
    process.exit(1);
  }
  console.log("Menu standard: PASS");
}
