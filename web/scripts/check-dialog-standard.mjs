// Dialog standard v1 (docs/design/dialogs.md): a dialog uses one of the shared
// sizes and its height follows its content. Screens may not size a dialog or
// build a modal by hand, and the shared tokens must keep the documented values.
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

/** The stylesheet that owns dialog geometry, and the component that owns modals. */
const owner = "components/ui/controls.css";
const component = "components/ui/dialog.tsx";

export const tokens = {
  "--vult-dialog-width-small": "480px",
  "--vult-dialog-width-medium": "640px",
  "--vult-dialog-width-large": "800px",
  "--vult-dialog-width-viewer": "1280px",
  "--vult-dialog-edge": "16px",
  "--vult-dialog-edge-phone": "8px",
};

const sizing = /(?:^|[;{\s])(?:min-|max-)?(?:width|height)\s*:/;

/** Class names screens pass to the shared Dialog. */
export function dialogClasses(source) {
  if (!/from\s+["'][^"']*ui\/dialog["']/.test(source)) return [];
  const names = [];
  for (const m of source.matchAll(/<Dialog\b[^>]*?className=(?:"([^"]*)"|\{`([^`]*)`)/gs))
    for (const name of (m[1] ?? m[2]).replace(/\$\{[^}]*\}/g, " ").split(/\s+/))
      if (/^[a-z][\w-]*$/.test(name)) names.push(name);
  return names;
}

function rules(css) {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, "");
  const found = [];
  for (const m of text.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selector = m[1].trim();
    if (!selector.startsWith("@")) found.push({ selector, body: m[2] });
  }
  return found;
}

/** Problems in one stylesheet; `file` is relative to src. */
export function checkStylesheet(file, css, classes = []) {
  if (file === owner) return [];
  const problems = [];
  const names = [...new Set(["dialog-content", ...classes])];
  for (const { selector, body } of rules(css)) {
    // The dialog itself, not something inside it.
    const subjects = selector.split(",").map((part) => part.trim().split(/\s+|>/).filter(Boolean).at(-1) ?? "");
    const isDialog = subjects.some((subject) => names.some((name) => new RegExp(`\\.${name}(?![\\w-])`).test(subject)));
    if (isDialog && sizing.test(body))
      problems.push(`"${selector.replace(/\s+/g, " ")}" sizes a dialog; choose a size on Dialog instead (docs/design/dialogs.md).`);
  }
  return problems;
}

/** Problems in one component; `file` is relative to src. */
export function checkSource(file, source) {
  if (file === component) return [];
  const problems = [];
  if (/from\s+["']@radix-ui\/react-dialog["']/.test(source) || /\bModalOverlay\b|<Modal\b|\saria-modal=/.test(source))
    problems.push("Build modals from the shared Dialog, not by hand (docs/design/dialogs.md).");
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

async function files(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  const nested = await Promise.all(entries.map((e) =>
    e.isDirectory() ? files(path.join(dir, e.name)) : /\.(?:tsx?|css)$/.test(e.name) ? [path.join(dir, e.name)] : []));
  return nested.flat();
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  const src = path.join(web, "src");
  const all = {};
  for (const file of await files(src)) all[path.relative(src, file).split(path.sep).join("/")] = await readFile(file, "utf8");
  const classes = Object.entries(all).filter(([f]) => /\.tsx?$/.test(f)).flatMap(([, text]) => dialogClasses(text));
  const failures = [];
  for (const [rel, text] of Object.entries(all)) {
    const problems = rel.endsWith(".css") ? checkStylesheet(rel, text, classes) : checkSource(rel, text);
    for (const p of problems) failures.push(`${rel}: ${p}`);
  }
  for (const p of checkTokens(all[owner])) failures.push(`controls.css: ${p}`);
  if (failures.length) {
    console.error(`Dialog standard: ${failures.length} problem(s)\n${failures.join("\n")}`);
    process.exit(1);
  }
  console.log("Dialog standard: PASS");
}
