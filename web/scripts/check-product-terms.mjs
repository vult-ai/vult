import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

// A Book is Vult's record of one person or organization. In interface copy it
// is a proper noun: "your Book", "Create Book", "Books". Lowercase "book" is
// left for code identifiers and the accounting term "book value".
const root = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../src",
);
const leads =
  "a an the this that your my one new each any another other current selected linked valid local open opening create creating switch switching manage import importing multiple different existing every no same whole which per".split(
    " ",
  );
// Lead words match in any case; "book" itself must be lowercase to fail.
const lead = leads
  .map((word) => `[${word[0]}${word[0].toUpperCase()}]${word.slice(1)}`)
  .join("|");
const lower = new RegExp(`\\b(?:${lead})\\s+books?\\b(?!\\s+value)`, "g");

export function checkSource(file, source) {
  const failures = [];
  source.split("\n").forEach((line, index) => {
    const code = line.trim();
    if (/^(?:\/\/|\/?\*)/.test(code)) return;
    for (const match of line.matchAll(lower)) {
      failures.push(
        `${file}:${index + 1}: write “${match[0].replace(/\bbook/, "Book")}” — Book is a proper noun in interface copy.`,
      );
    }
  });
  return failures;
}

function scan(dir) {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const file = path.join(dir, entry.name);
    if (entry.isDirectory())
      return file.includes(`${path.sep}catalog`) ? [] : scan(file);
    return /\.tsx?$/.test(entry.name) ? [file] : [];
  });
}

if (
  process.argv[1] &&
  path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)
) {
  const failures = scan(root).flatMap((file) =>
    checkSource(path.relative(root, file), fs.readFileSync(file, "utf8")),
  );
  if (failures.length) {
    process.stderr.write(failures.join("\n") + "\n");
    process.exitCode = 1;
  } else process.stdout.write("Product terms: PASS\n");
}
