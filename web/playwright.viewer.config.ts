import { defineConfig } from "@playwright/test";
import base from "./playwright.config";

export default defineConfig({
  ...base,
  testDir: "./preview-tests",
  use: { ...base.use, baseURL: process.env.VULT_E2E_URL || "http://127.0.0.1:5173" },
});
