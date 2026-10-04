import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./tests",
  testMatch: "**/*.spec.ts",
  workers: 1,
  timeout: 60000,
  expect: { timeout: 10000 },
  use: {
    baseURL: process.env.QUIVR_DEMO_URL || "http://127.0.0.1:5182",
    trace: "retain-on-failure",
  },
  outputDir: process.env.QUIVR_DEMO_ARTIFACTS || "../.scratch/demo-browser",
  reporter: "list",
});
