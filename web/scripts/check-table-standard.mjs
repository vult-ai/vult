// Table standard v1 (docs/design/tables.md): a column starts at the width its
// kind gives it, and every data column can be resized independently.
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const table = "design-system/catalog/components/application/table/records-table.tsx";
export const expected = { date: 112, money: 160, status: 128, text: 120, selection: 32 };

/** Problems in one source file; `file` is relative to src. */
export function checkSource(file, source) {
  const problems = [];
  if (file === table) return problems;
  // Column objects: an id, a label and a kind.
  for (const m of source.matchAll(/\{([^{}]*?\bid:\s*"([^"]+)"(?:[^{}]|\$\{[^{}]*\})*?\bkind:\s*"(\w+)"(?:[^{}]|\$\{[^{}]*\})*?)\}/gs)) {
    const [, body, id, kind] = m;
    if (/\bgrow:/.test(body)) problems.push(`Column "${id}": growth follows its kind; remove grow.`);
    if (["date", "money", "status"].includes(kind) && /\b(?:minWidth|width):/.test(body))
      problems.push(`Column "${id}": ${kind} columns take the shared ${kind} width; remove width and minWidth.`);
    const min = /\bminWidth:\s*(\d+)/.exec(body);
    if (kind === "text" && min && Number(min[1]) < expected.text)
      problems.push(`Column "${id}": text columns are at least ${expected.text}px wide.`);
  }
  return problems;
}

/** The kind widths in the records table match the document. */
export function checkKinds(source) {
  const problems = [];
  const kinds = /COLUMN_KIND_WIDTHS\s*=\s*\{\s*date:\s*(\d+),\s*money:\s*(\d+),\s*status:\s*(\d+)\s*\}/.exec(source);
  const text = /TEXT_COLUMN_MIN_WIDTH\s*=\s*(\d+)/.exec(source);
  const slot = /SELECTION_SLOT_WIDTH\s*=\s*(\d+)/.exec(source);
  if (!kinds || !text || !slot) return ["The records table no longer declares its column kinds."];
  const actual = { date: +kinds[1], money: +kinds[2], status: +kinds[3], text: +text[1], selection: +slot[1] };
  for (const [name, value] of Object.entries(expected))
    if (actual[name] !== value) problems.push(`${name} width is ${actual[name]}px; the standard says ${value}px.`);
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
  for (const p of checkKinds(await readFile(path.join(src, table), "utf8"))) failures.push(`${table}: ${p}`);
  if (failures.length) {
    console.error(`Table standard: ${failures.length} problem(s)\n${failures.join("\n")}`);
    process.exit(1);
  }
  console.log("Table standard: PASS");
}
