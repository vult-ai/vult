import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";
import postcss from "postcss";

const expected = {
  metadata: "12px",
  label: "14px",
  ui: "14px",
  body: "16px",
  "compact-title": "16px",
  "section-title": "20px",
  "metric-value": "20px",
  "page-title": "30px",
  "dashboard-value": "24px",
  log: "12px",
  "log-value": "14px",
  "log-metric": "20px",
};
const roles = new Set(
  Object.keys(expected).map((role) => `var(--text-vult-${role})`),
);
const key = (decl) =>
  `${decl.prop.toLowerCase()}:${decl.value.replace(/\s+/g, " ").trim()}`;
export function unapproved(source, file) {
  const found = [];
  postcss.parse(source, { from: file }).walkDecls((decl) => {
    const prop = decl.prop.toLowerCase(),
      value = decl.value.trim();
    if (!["font-size", "font"].includes(prop)) return;
    if (
      ["inherit", "initial", "unset", "revert", "revert-layer"].includes(value)
    )
      return;
    if (prop === "font-size" && roles.has(value)) return;
    found.push({ key: key(decl), line: decl.source.start.line });
  });
  return found;
}
export function validateTypography({ theme, product, files, legacy }) {
  const tokens = new Map();
  for (const css of [theme, product])
    postcss
      .parse(css)
      .walkDecls((decl) => tokens.set(decl.prop, decl.value.trim()));
  const errors = [];
  for (const [role, value] of Object.entries(expected)) {
    if (tokens.get(`--text-vult-${role}`) !== value)
      errors.push(`Typography role ${role} must be ${value}.`);
  }
  for (const { file, source } of files) {
    const counts = {};
    for (const declaration of unapproved(source, file)) {
      const n = (counts[declaration.key] = (counts[declaration.key] || 0) + 1);
      if (n > (legacy[file]?.[declaration.key] || 0))
        errors.push(
          `${file}:${declaration.line}: use a semantic typography role instead of ${declaration.key}.`,
        );
    }
  }
  if (errors.length) throw new Error(errors.join("\n"));
}
export async function applicationStyles(root) {
  const files = [];
  async function walk(dir) {
    for (const entry of await readdir(path.join(root, dir), {
      withFileTypes: true,
    })) {
      const file = `${dir}/${entry.name}`;
      if (entry.isDirectory()) await walk(file);
      else if (file.endsWith(".css"))
        files.push({
          file,
          source: await readFile(path.join(root, file), "utf8"),
        });
    }
  }
  await walk("src/components");
  await walk("src/styles");
  files.push({
    file: "src/style.css",
    source: await readFile(path.join(root, "src/style.css"), "utf8"),
  });
  return files.sort((a, b) => a.file.localeCompare(b.file));
}
if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  const root = path.resolve(import.meta.dirname, "..");
  const [theme, product, files, legacy] = await Promise.all([
    readFile(path.join(root, "src/design-system/styles/theme.css"), "utf8"),
    readFile(
      path.join(root, "src/design-system/styles/product-typography.css"),
      "utf8",
    ),
    applicationStyles(root),
    readFile(path.join(root, "typography-legacy.json"), "utf8").then(
      JSON.parse,
    ),
  ]);
  validateTypography({ theme, product, files, legacy });
  console.log(
    `Verified six-size typography roles and ${files.length} first-party stylesheets; legacy declarations cannot increase.`,
  );
}
