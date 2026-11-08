// App shell standard v1 (docs/design/shell.md): every page sits in the same
// rail, page bar and canvas. Pages may not restyle the frame, the shell tokens
// must keep the documented values, and the frame has no deployment modes.
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

export const tokens = {
  "--vult-shell-rail-width": "256px",
  "--vult-shell-rail-collapsed-width": "64px",
  "--vult-shell-row-height": "var(--vult-control-height-sm)",
  "--vult-shell-row-gap": "4px",
  "--vult-shell-header-height": "56px",
  "--vult-shell-header-gutter": "16px",
  "--vult-shell-header-gutter-wide": "24px",
  "--vult-shell-header-content-gap": "8px",
  "--vult-shell-switcher-max-width": "180px",
  "--vult-shell-switcher-phone-max-width": "148px",
  "--vult-workspace-max-width": "1440px",
  "--vult-workspace-gutter": "16px",
};

/** Stylesheets that own the frame. */
const owners = ["components/shell/", "design-system/"];

/** Page stylesheets that still adjust the frame, each with the reason it may. */
export const exceptions = {
  // Statements lays out as a collection page but is not yet passed `collection`.
  "components/statements/statement-tools.css": "statements canvas",
  // Sources clips its canvas so focus never scrolls the pane behind the stage.
  "components/book/source-stage.css": "sources canvas clip",
};

const frame =
  /\.(?:vult-app-shell|shell-canvas|shell-scroll-region|vult-shell-(?:header|rail|rail-[a-z-]+|panel|actions|workspace-control|group))(?![\w-])/;
// Print styles may hide or flatten the frame.
const print = /data-vult-report(?:-pack)?-print/;
const retired = /data-vult-deployment|vult-support-|vult-shell-preview/;

function selectors(css) {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, "");
  const found = [];
  let inPrint = 0;
  let depth = 0;
  const printDepths = [];
  for (const m of text.matchAll(/([^{};]*)([{}])/g)) {
    if (m[2] === "}") {
      depth -= 1;
      if (printDepths.at(-1) === depth) printDepths.pop();
      inPrint = printDepths.length;
      continue;
    }
    const head = m[1].trim();
    if (head.startsWith("@")) {
      if (/^@media\s+print\b/.test(head)) printDepths.push(depth);
      inPrint = printDepths.length;
    } else if (head) found.push({ selector: head, print: inPrint > 0 });
    depth += 1;
  }
  return found;
}

/** Problems in one stylesheet; `file` is relative to src. */
export function checkStylesheet(file, css) {
  const problems = [];
  if (retired.test(css.replace(/\/\*[\s\S]*?\*\//g, "")))
    problems.push("The frame has one mode; remove deployment-mode and support-panel styles (docs/design/shell.md).");
  if (owners.some((prefix) => file.startsWith(prefix)) || exceptions[file]) return problems;
  for (const { selector, print: printing } of selectors(css)) {
    if (printing || print.test(selector)) continue;
    if (frame.test(selector))
      problems.push(`"${selector.replace(/\s+/g, " ")}" styles the app frame; pages own only the canvas content (docs/design/shell.md).`);
  }
  return problems;
}

/** Problems in one script or component. */
export function checkSource(source) {
  return retired.test(source)
    ? ["The frame has one mode; remove deployment-mode and support-panel markup (docs/design/shell.md)."]
    : [];
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

async function files(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  const nested = await Promise.all(entries.map((e) =>
    e.isDirectory() ? files(path.join(dir, e.name)) : /\.(?:tsx?|css)$/.test(e.name) ? [path.join(dir, e.name)] : []));
  return nested.flat();
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  const src = path.join(web, "src");
  const failures = [];
  for (const file of await files(src)) {
    const rel = path.relative(src, file).split(path.sep).join("/");
    const text = await readFile(file, "utf8");
    const problems = rel.endsWith(".css") ? checkStylesheet(rel, text) : checkSource(text);
    for (const p of problems) failures.push(`${rel}: ${p}`);
  }
  for (const p of checkTokens(await readFile(path.join(src, "design-system/styles/shell-rail.css"), "utf8")))
    failures.push(`shell-rail.css: ${p}`);
  if (failures.length) {
    console.error(`Shell standard: ${failures.length} problem(s)\n${failures.join("\n")}`);
    process.exit(1);
  }
  console.log("Shell standard: PASS");
}
