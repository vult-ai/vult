import { expect, test } from "@playwright/test";

// The development server serves the real viewer with synthetic originals.
// Run with VULT_E2E_URL pointing at that server (see CONTRIBUTING.md).
const cases = [
  ...[320, 390, 430, 1280].flatMap((width) => ["preview", "recall"].map((presentation) => ({ width, presentation, kind: "pdf" }))),
  { width: 390, presentation: "preview", kind: "image" },
];
for (const { width, presentation, kind } of cases) {
    test(`${kind} controls keep their own space at ${width}px in ${presentation}`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 });
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await page.goto(`/tests/fixtures/document-viewer.html?theme=dark&presentation=${presentation}&kind=${kind}`);
      const viewer = page.locator(".document-preview-viewer");
      await expect(viewer.locator(".document-viewer-surface")).toHaveAttribute("aria-busy", "false");
      await expect(viewer.getByLabel("Document controls")).toContainText("1 / 1");
      const geometry = await viewer.evaluate((node) => {
        const viewport = node.querySelector(".document-viewer-viewport")!.getBoundingClientRect();
        const controls = node.querySelector(".document-viewer-controls")!.getBoundingClientRect();
        const expand = node.querySelector('button[aria-label="Expand document"]')!.getBoundingClientRect();
        const canvas = node.querySelector("canvas")!;
        const pixels = canvas.getContext("2d")!.getImageData(0, 0, canvas.width, canvas.height).data;
        let darkPixels = 0;
        for (let i = 0; i < pixels.length; i += 4)
          if (pixels[i + 3] && pixels[i] < 200 && pixels[i + 1] < 200 && pixels[i + 2] < 200) darkPixels++;
        return { bottom: viewport.bottom, controlsTop: controls.top, expand: { width: expand.width, height: expand.height }, darkPixels,
          overflow: document.documentElement.scrollWidth > innerWidth };
      });
      expect(geometry.controlsTop).toBeGreaterThanOrEqual(geometry.bottom - 1);
      expect(geometry.darkPixels).toBeGreaterThan(100);
      expect(geometry.overflow).toBe(false);
      if (width < 640) {
        expect(geometry.expand.width).toBeGreaterThanOrEqual(44);
        expect(geometry.expand.height).toBeGreaterThanOrEqual(44);
      }
      await viewer.getByRole("button", { name: "Expand document" }).click();
      const dialog = page.getByRole("dialog", { name: kind === "image" ? "Example image" : "Example statement" });
      await expect(dialog).toBeVisible();
      await expect(dialog.locator(".document-viewer-surface")).toHaveAttribute("aria-busy", "false");
      await expect(dialog.getByRole("button", { name: "Fit document to width" })).toBeEnabled();
      await dialog.getByRole("button", { name: "Close dialog", exact: true }).click();
      await expect(dialog).toBeHidden();
      await expect(viewer.getByRole("button", { name: "Expand document" })).toBeFocused();
      expect(errors).toEqual([]);
    });
}
