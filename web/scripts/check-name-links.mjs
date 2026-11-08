import { readFileSync, readdirSync } from "node:fs";
import { resolve, join } from "node:path";
import { fileURLToPath } from "node:url";
import parser from "prettier/plugins/typescript";
import postcss from "postcss";
const root = resolve(
  process.argv[2] || fileURLToPath(new URL("../", import.meta.url)),
);
const legacy =
  /\b(entry-link|v0-row-link|v0-detail-link|statement-entry-link|statement-review-link)\b/;
let links = 0;
function visit(node, source, file) {
  if (!node || typeof node !== "object") return;
  if (node.type === "JSXOpeningElement") {
    const attr = node.attributes.find((a) => a.name?.name === "className");
    if (attr && legacy.test(source.slice(...attr.range))) {
      if (node.name.name !== "NameLink")
        throw Error(
          `${file}: record links must use NameLink so decorations cannot be underlined`,
        );
      links++;
    }
  }
  for (const [key, value] of Object.entries(node)) {
    if (["loc", "range", "tokens", "comments"].includes(key)) continue;
    if (Array.isArray(value)) value.forEach((v) => visit(v, source, file));
    else if (value && typeof value === "object") visit(value, source, file);
  }
}
function walk(dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const file = join(dir, entry.name);
    if (entry.isDirectory()) walk(file);
    else if (file.endsWith(".tsx")) {
      const source = readFileSync(file, "utf8");
      visit(
        parser.parsers.typescript.parse(source, { filepath: file }),
        source,
        file,
      );
    } else if (file.endsWith(".css") && !file.endsWith("/ui/name-link.css")) {
      postcss
        .parse(readFileSync(file, "utf8"))
        .walkDecls(/^text-decoration(?:-line)?$/, (decl) => {
          if (
            /underline/.test(decl.value) &&
            legacy.test(decl.parent.selector || "")
          )
            throw Error(
              `${file}: record-link hover belongs to the shared NameLink label`,
            );
        });
    }
  }
}
walk(join(root, "src/components"));
for (const entry of readdirSync(join(root, "src/styles"))) {
  if (!entry.endsWith(".css")) continue;
  const file = join(root, "src/styles", entry);
  postcss
    .parse(readFileSync(file, "utf8"))
    .walkDecls(/^text-decoration(?:-line)?$/, (decl) => {
      if (
        /underline/.test(decl.value) &&
        legacy.test(decl.parent.selector || "")
      )
        throw Error(
          `${file}: global record-link underlines are forbidden; use NameLink`,
        );
    });
}
console.log(
  `Verified ${links} shared name links; no page or global composite underline rules.`,
);
