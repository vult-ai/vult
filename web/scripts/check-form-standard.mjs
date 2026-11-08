// Form standard v1 (docs/design/forms.md): every native control is drawn by
// Field, NativeFieldControl or one of the shared controls, so each field has
// the same label, hint and error.
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

/** The shared controls, which draw native elements themselves. */
export const owners = new Set([
  "components/form.tsx",
  "components/ui/select.tsx",
  "components/ui/combobox.tsx",
  "components/ui/native-checkbox.tsx",
  "components/ui/native-field-control.tsx",
  "components/ui/book-url-input.tsx",
  // A hidden native select relays the inspector Select's change event.
  "components/statements/statement-tools.tsx",
]);

const wrappers = ["Field", "NativeFieldControl"];

/** Problems in one component; `file` is relative to src. */
export function checkSource(file, source) {
  if (owners.has(file) || file.startsWith("design-system/catalog/")) return [];
  const problems = [];
  for (const m of source.matchAll(/<(input|select|textarea)\b([^>]*)/g)) {
    const type = /\stype=["']([a-z]+)["']/.exec(m[2])?.[1];
    if (["checkbox", "radio", "hidden", "file"].includes(type)) continue;
    const before = source.slice(0, m.index);
    const open = wrappers.some((name) =>
      (before.match(new RegExp(`<${name}\\b`, "g")) || []).length >
      (before.match(new RegExp(`</${name}>`, "g")) || []).length);
    if (!open) {
      const line = before.split("\n").length;
      problems.push(`line ${line}: wrap this <${m[1]}> in Field or NativeFieldControl so it has the shared label, hint and error (docs/design/forms.md).`);
    }
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
  const failures = [];
  for (const file of await sources(src)) {
    const rel = path.relative(src, file).split(path.sep).join("/");
    for (const p of checkSource(rel, await readFile(file, "utf8"))) failures.push(`${rel}: ${p}`);
  }
  if (failures.length) {
    console.error(`Form standard: ${failures.length} problem(s)\n${failures.join("\n")}`);
    process.exit(1);
  }
  console.log("Form standard: PASS");
}
