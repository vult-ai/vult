import fs from "node:fs/promises";
import path from "node:path";
import { execFileSync } from "node:child_process";
import postcss from "postcss";

const root = path.resolve(import.meta.dirname, "..");
async function files(directory) {
  const entries = await fs.readdir(directory, { withFileTypes: true });
  return (
    await Promise.all(
      entries.map((e) =>
        e.isDirectory()
          ? files(path.join(directory, e.name))
          : path.join(directory, e.name),
      ),
    )
  ).flat();
}
const sourceFiles = (await files(path.join(root, "src"))).filter((f) =>
  /\.(css|tsx?)$/.test(f),
);
const cssFiles = sourceFiles.filter(
  (f) => f.endsWith(".css") && !f.endsWith("neutral-surfaces.css"),
);
const declarations = [];
const uses = [];
const refNames = (value) =>
  [...value.matchAll(/var\((--[\w-]+)/g)].map((m) => m[1]);
const modeOf = (selector) =>
  /dark-mode|appearance=["']?dark/.test(selector)
    ? "dark"
    : /light-mode|appearance=["']?light/.test(selector)
      ? "light"
      : "base";
function blueRGB(r, g, b) {
  const max = Math.max(r, g, b),
    min = Math.min(r, g, b),
    delta = max - min;
  if (delta < 0.5) return false;
  let h =
    max === r
      ? 60 * (((g - b) / delta) % 6)
      : max === g
        ? 60 * ((b - r) / delta + 2)
        : 60 * ((r - g) / delta + 4);
  if (h < 0) h += 360;
  return h >= 190 && h <= 290;
}
function hasBlue(value) {
  for (const m of value.matchAll(/#([\da-f]{3,8})\b/gi)) {
    let h = m[1];
    if (h.length === 3 || h.length === 4) h = [...h].map((c) => c + c).join("");
    if (h.length !== 6 && h.length !== 8) continue;
    if (h.length === 8 && parseInt(h.slice(6), 16) === 0) continue;
    if (blueRGB(...[0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16))))
      return true;
  }
  for (const m of value.matchAll(/rgba?\(([^)]+)\)/g)) {
    const parts = m[1].split(/[\s,/]+/).filter(Boolean);
    if (
      parts.length >= 3 &&
      Number(parts[3] ?? 1) !== 0 &&
      blueRGB(
        ...parts
          .slice(0, 3)
          .map((x) => parseFloat(x) * (x.endsWith("%") ? 2.55 : 1)),
      )
    )
      return true;
  }
  for (const m of value.matchAll(/oklch\(\s*[\d.%]+\s+([\d.]+)\s+([\d.]+)/g)) {
    if (Number(m[1]) > 0 && Number(m[2]) >= 190 && Number(m[2]) <= 290)
      return true;
  }
  return false;
}
const baselineIndex = process.argv.indexOf("--baseline");
const baseline = baselineIndex >= 0 ? process.argv[baselineIndex + 1] : "HEAD";
if (!baseline) throw new Error("--baseline requires a Git revision");
const baseCommit = execFileSync("git", ["rev-parse", baseline], {
  cwd: root,
  encoding: "utf8",
}).trim();
for (const file of [
  path.join(root, "node_modules/tailwindcss/theme.css"),
  ...cssFiles,
]) {
  let text = await fs.readFile(file, "utf8");
  // Preserve the exact pre-change selection declaration in this audit.
  if (file.endsWith("text-selection.css"))
    text = execFileSync(
      "git",
      ["show", `${baseCommit}:web/src/design-system/text-selection.css`],
      { cwd: root, encoding: "utf8" },
    );
  const relative = path.relative(root, file);
  postcss.parse(text).walkDecls((d) => {
    let p = d.parent;
    const selectors = [];
    while (p) {
      if (p.selector) selectors.unshift(p.selector);
      p = p.parent;
    }
    const selector = selectors.join(" ") || "@theme";
    const entry = {
      value: d.value,
      source: `${relative}:${d.source.start.line}`,
      selector,
      mode: modeOf(selector),
      refs: refNames(d.value),
    };
    if (d.prop.startsWith("--")) declarations.push({ token: d.prop, ...entry });
    else if (entry.refs.length || hasBlue(d.value))
      uses.push({ property: d.prop, ...entry });
  });
}
const byName = new Map();
for (const d of declarations) {
  if (!byName.has(d.token)) byName.set(d.token, []);
  byName.get(d.token).push(d);
}
function candidates(name, mode) {
  const entries = byName.get(name) || [];
  const explicit = entries.filter((d) => d.mode === mode);
  return explicit.length ? explicit : entries.filter((d) => d.mode === "base");
}
function blueToken(name, mode, seen = new Set()) {
  if (seen.has(name)) return false;
  const next = new Set(seen).add(name);
  return candidates(name, mode).some(
    (d) => hasBlue(d.value) || d.refs.some((ref) => blueToken(ref, mode, next)),
  );
}
const overrides = new Set();
postcss
  .parse(
    await fs.readFile(
      path.join(root, "src/design-system/neutral-surfaces.css"),
      "utf8",
    ),
  )
  .walkDecls((d) => overrides.add(d.prop));
overrides.add("--vult-selection-background");
function affected(name, seen = new Set()) {
  if (overrides.has(name)) return true;
  if (seen.has(name)) return false;
  const next = new Set(seen).add(name);
  return (byName.get(name) || []).some((d) =>
    d.refs.some((r) => affected(r, next)),
  );
}
function category(name) {
  if (/^--color-(slate|gray|zinc)-/.test(name))
    return "Available cool-neutral palette";
  if (/^--color-(brand|blue|sky|cyan|indigo|violet)-/.test(name))
    return "Available chromatic palette";
  if (/^--color-utility-/.test(name)) return "Utility palette alias";
  if (/shadow|glow|(?:^|-)rim(?:$|-)/.test(name))
    return "Shadow, rim or interaction effect";
  if (/selection/.test(name)) return "Text selection";
  if (/bg-|background/.test(name)) return "Surface / background alias";
  if (/border|ring|outline/.test(name)) return "Border / focus alias";
  return "Text / foreground / component alias";
}
const tokens = [...byName.keys()].sort().flatMap((token) => {
  const lightBlue = blueToken(token, "light"),
    darkBlue = blueToken(token, "dark");
  if (!lightBlue && !darkBlue) return [];
  return [
    {
      token,
      category: category(token),
      lightBlue,
      darkBlue,
      light: candidates(token, "light"),
      dark: candidates(token, "dark"),
      neutralizedDirectlyOrThroughAlias: affected(token),
    },
  ];
});
const directUses = uses.filter(
  (d) =>
    hasBlue(d.value) ||
    d.refs.some(
      (r) =>
        blueToken(r, d.mode === "base" ? "light" : d.mode) ||
        (d.mode === "base" && blueToken(r, "dark")),
    ),
);
for (const file of sourceFiles.filter((f) => /\.tsx?$/.test(f))) {
  const text = await fs.readFile(file, "utf8");
  for (const [i, line] of text.split("\n").entries()) {
    const refs = refNames(line);
    const utility =
      /(?:bg|text|border|ring|outline|stroke|fill)-(?:brand|blue|sky|cyan|slate|gray|zinc|indigo|violet)-[\w-]+/.test(
        line,
      );
    if (
      utility ||
      refs.some((r) => blueToken(r, "light") || blueToken(r, "dark"))
    )
      directUses.push({
        source: `${path.relative(root, file)}:${i + 1}`,
        value: line.trim(),
        refs,
        mode: "component",
      });
  }
}
const output = {
  basis: baseCommit,
  scope:
    "All shipped application CSS declarations, their transitive token aliases, Tailwind's available palette, and direct CSS/TSX consumers. Source declarations are preserved; context-specific overrides are shown separately. Availability does not mean a token is currently rendered.",
  tokens,
  directUses,
};
const destination = path.resolve(root, "../docs/color-token-audit.json");
await fs.writeFile(destination, JSON.stringify(output, null, 2) + "\n");
const csv = (value) => '"' + String(value).replaceAll('"', '""') + '"';
const rows = [
  [
    "token",
    "category",
    "blue_in_light",
    "blue_in_dark",
    "light_definitions",
    "dark_definitions",
    "neutralized_directly_or_via_alias",
    "source_locations",
  ],
  ...tokens.map((t) => [
    t.token,
    t.category,
    t.lightBlue,
    t.darkBlue,
    [...new Set(t.light.map((d) => d.value))].join(" | "),
    [...new Set(t.dark.map((d) => d.value))].join(" | "),
    t.neutralizedDirectlyOrThroughAlias,
    [...new Set([...t.light, ...t.dark].map((d) => d.source))].join(" | "),
  ]),
];
await fs.writeFile(
  path.resolve(root, "../docs/color-token-audit.csv"),
  rows.map((row) => row.map(csv).join(",")).join("\n") + "\n",
);
console.log(
  JSON.stringify({
    tokens: tokens.length,
    light: tokens.filter((t) => t.lightBlue).length,
    dark: tokens.filter((t) => t.darkBlue).length,
    directUses: directUses.length,
  }),
);
