import { defineConfig } from "@playwright/test";
// Local browser work uses the owner's configured Chrome plugin. CI may launch
// its disposable Chromium. An explicit flag enables a contributor's fresh local browser.
// VULT_E2E_CHANNEL=chrome runs an installed Chrome with a temporary profile.
const channel = process.env.VULT_E2E_CHANNEL;
if (!process.env.CI && !channel && process.env.VULT_E2E_ALLOW_NEW_BROWSER !== "1")
  throw new Error(
    "Use the configured Chrome plugin locally, VULT_E2E_CHANNEL=chrome for an installed Chrome, or VULT_E2E_ALLOW_NEW_BROWSER=1 for a disposable Playwright browser.",
  );
export default defineConfig({
  testDir: "./tests",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 60000,
  use: {
    baseURL: process.env.VULT_E2E_URL || "http://127.0.0.1:8080",
    browserName: "chromium",
    ...(channel ? { channel } : {}),
    viewport: { width: 1280, height: 900 },
    trace: "retain-on-failure",
  },
  reporter: "list",
});
