// Institution and brand artwork lives only at the shared asset origin. This check
// fails if a copy of it, or its catalogs, appears in the repository or a build.
// Synthetic test artwork under unit/fixtures is the one allowed exception.
import { readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repo = fileURLToPath(new URL("../../", import.meta.url));
const catalogs = new Set(["catalog.v1.json", "brand-logos.v1.json", "brands.v1.json"]);
const original = /^[a-f0-9]{64}\.(?:svg|png|webp)$/;
const skip = new Set(["node_modules", ".git", "fixtures"]);
const found = [];

async function walk(folder) {
  let entries;
  try {
    entries = await readdir(folder, { withFileTypes: true });
  } catch {
    return;
  }
  for (const entry of entries) {
    const full = path.join(folder, entry.name);
    if (entry.isDirectory()) {
      if (entry.name === "shared-assets") found.push(full);
      else if (!skip.has(entry.name)) await walk(full);
    } else if (catalogs.has(entry.name) || original.test(entry.name)) found.push(full);
  }
}

await walk(repo);
if (process.argv.includes("--production")) await walk(fileURLToPath(new URL("../dist/", import.meta.url)));
if (found.length)
  throw Error(`Institution artwork belongs at the shared asset origin, not here:\n${found.map((f) => `  ${path.relative(repo, f)}`).join("\n")}`);
console.log(`Verified no institution artwork or catalogs in the repository${process.argv.includes("--production") ? " or the build" : ""}.`);
