// Visual token standard v1 (docs/design/tokens.md): component stylesheets take
// corner radii and colours from tokens, and icons come from one library. Older
// literal values are listed in visual-legacy.json; there may never be more of
// them, so the list only shrinks.
import { readFile, readdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const legacyFile = path.join(web, "visual-legacy.json");

/** The design system defines the tokens, so it may use literal values. */
export const tokenOwners = (file) => file.startsWith("design-system/");

const literalRadius = /border(?:-[a-z]+)*-radius\s*:\s*([^;]+)/g;
const allowedRadius = /^(?:0|0px|50%|inherit|var\([^)]*\)|calc\(.*var\(.*\).*\))(?:\s*!important)?$/;
const rawColour = /#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?|oklch|oklab|lab|lch)\(/g;

/** Counts of literal radii and raw colours in one stylesheet. */
export function count(css) {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, "");
  let radius = 0;
  for (const [, value] of text.matchAll(literalRadius))
    if (!allowedRadius.test(value.trim())) radius++;
  // Custom property definitions are tokens, not uses.
  const uses = text.replace(/--[\w-]+\s*:[^;]+;/g, "");
  const colour = (uses.match(rawColour) || []).length;
  return { radius, colour };
}

/** Problems for one stylesheet against its legacy allowance. */
export function checkStylesheet(file, css, legacy = {}) {
  if (tokenOwners(file)) return [];
  const found = count(css);
  const allowed = legacy[file] || { radius: 0, colour: 0 };
  const problems = [];
  if (found.radius > allowed.radius)
    problems.push(`${found.radius - allowed.radius} new literal corner radius; use a --radius-* token (docs/design/tokens.md).`);
  if (found.colour > allowed.colour)
    problems.push(`${found.colour - allowed.colour} new raw colour; use a colour token (docs/design/tokens.md).`);
  return problems;
}

/** Problems for one component: icons come from the one icon module, which alone
 * imports the library (Phosphor), so the library can change in one place. */
export function checkSource(source, file = "") {
  if (/(^|\/)(src\/)?design-system\/icons\.tsx$/.test(file)) return [];
  return /from\s+["'](?:lucide-react|react-icons[^"']*|@heroicons\/[^"']+|@phosphor-icons\/[^"']+|@tabler\/icons[^"']*|@untitledui\/icons[^"']*)["']/.test(source)
    ? ["Icons come from src/design-system/icons.tsx only (docs/design/tokens.md)."]
    : [];
}

async function files(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  const nested = await Promise.all(entries.map((e) =>
    e.isDirectory() ? files(path.join(dir, e.name)) : /\.(?:css|tsx?)$/.test(e.name) ? [path.join(dir, e.name)] : []));
  return nested.flat();
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  const src = path.join(web, "src");
  const all = {};
  for (const file of await files(src)) all[path.relative(src, file).split(path.sep).join("/")] = await readFile(file, "utf8");
  if (process.argv.includes("--write-legacy")) {
    const legacy = {};
    for (const [file, text] of Object.entries(all)) {
      if (!file.endsWith(".css") || tokenOwners(file)) continue;
      const found = count(text);
      if (found.radius || found.colour) legacy[file] = found;
    }
    await writeFile(legacyFile, JSON.stringify(legacy, null, 2) + "\n");
    console.log(`Wrote ${Object.keys(legacy).length} legacy entries.`);
    process.exit(0);
  }
  const legacy = JSON.parse(await readFile(legacyFile, "utf8"));
  const failures = [];
  for (const [file, text] of Object.entries(all)) {
    const problems = file.endsWith(".css") ? checkStylesheet(file, text, legacy) : checkSource(text, file);
    for (const p of problems) failures.push(`${file}: ${p}`);
  }
  if (failures.length) {
    console.error(`Visual tokens: ${failures.length} problem(s)\n${failures.join("\n")}`);
    process.exit(1);
  }
  const left = Object.values(legacy).reduce((sum, v) => ({ radius: sum.radius + v.radius, colour: sum.colour + v.colour }), { radius: 0, colour: 0 });
  console.log(`Visual tokens: PASS (legacy: ${left.radius} literal radii, ${left.colour} raw colours; these can only shrink)`);
}
