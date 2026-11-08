import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import {
  cp,
  mkdtemp,
  mkdir,
  readFile,
  realpath,
  rm,
  symlink,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import test from "node:test";

const run = promisify(execFile);

test("the build gate rejects the previous page, theme and responsive sizing regressions", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "vult-avatar-standard-"));
  const files = [
    "scripts/check-account-avatar-standard.mjs",
    "src/design-system/account-avatar-standard.ts",
    "src/components/ui/account-avatar.css",
  ];
  try {
    for (const file of files) {
      await mkdir(path.dirname(path.join(root, file)), { recursive: true });
      await cp(file, path.join(root, file));
    }
    await symlink(
      await realpath("node_modules"),
      path.join(root, "node_modules"),
      "dir",
    );
    const script = path.join(root, files[0]);
    const owner = path.join(root, files[2]);
    const css = await readFile(owner, "utf8");
    assert.match(
      (await run(process.execPath, [script])).stdout,
      /40px circle \/ 24px circular safe area \/ 8px radial clearance/,
    );
    const page = path.join(root, "src/page.css");
    for (const selector of [
      ".account-avatar",
      "[data-account-avatar]",
      "[data-counterparty-avatar]",
    ]) {
      await writeFile(page, `${selector} { width: 32px; }`);
      await assert.rejects(
        run(process.execPath, [script]),
        /Account icon styling has one owner/,
      );
      await rm(page);
    }
    const bypass = path.join(root, "src/page.tsx");
    await writeFile(
      bypass,
      'import { InstitutionLogo } from "./components/ui/institution-logo";',
    );
    await assert.rejects(run(process.execPath, [script]), /Use AccountAvatar/);
    await rm(bypass);
    for (const [bad, message] of [
      [
        css.replace("width: var(--account-avatar-size);", "width: 32px;"),
        /must follow the shared contract/,
      ],
      [
        css + "\n.dark-mode .institution-logo { padding: 4px; }",
        /may change color only/,
      ],
      [
        css + "\n@media (width < 640px) { .account-avatar { width: 28px; } }",
        /cannot have responsive/,
      ],
      [
        css.replaceAll("scale(var(--institution-circle-fit))", "scale(1)"),
        /must follow the shared contract/,
      ],
      [
        css.replace(
          /(\.institution-logo-viewport \{[^}]*?)overflow: hidden;/,
          "$1overflow: visible;",
        ),
        /must follow the shared contract/,
      ],
    ]) {
      await writeFile(owner, bad);
      await assert.rejects(run(process.execPath, [script]), message);
    }
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
