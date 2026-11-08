import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

export function dateFormatViolations(source) {
  return [
    [/\bIntl\.DateTimeFormat\s*\(/g, "Intl.DateTimeFormat"],
    [/\.toLocaleDateString\s*\(/g, "toLocaleDateString"],
    [/\buseDateFormatter\s*\(/g, "useDateFormatter"],
    [/new\s+Date\s*\([^;]*?\)\s*\.toLocaleString\s*\(/g, "Date.toLocaleString"],
  ].flatMap(([pattern, name]) => [...source.matchAll(pattern)].map((match) => ({
    name,
    line: source.slice(0, match.index).split("\n").length,
  })));
}

async function check() {
  const root = path.resolve(import.meta.dirname, "..");
  const files = [path.join(root, "src/main.tsx")];
  async function walk(directory) {
    for (const entry of await readdir(directory, { withFileTypes: true })) {
      const file = path.join(directory, entry.name);
      if (entry.isDirectory()) await walk(file);
      else if (/\.tsx?$/.test(entry.name)) files.push(file);
    }
  }
  await walk(path.join(root, "src/components"));
  const errors = [];
  for (const file of files) {
    const source = await readFile(file, "utf8");
    for (const violation of dateFormatViolations(source))
      errors.push(`${path.relative(root, file)}:${violation.line}: ${violation.name}`);
  }
  if (errors.length)
    throw Error(`Use RecordDate or lib/date-format for first-party dates:\n${errors.join("\n")}`);
  console.log(`Verified ${files.length} first-party components contain no independent date formatters.`);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url))
  await check();

export { check as checkDateFormat };
