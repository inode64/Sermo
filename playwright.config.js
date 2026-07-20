const { defineConfig, devices } = require("@playwright/test");

module.exports = defineConfig({
  testDir: "./tests/web",
  outputDir: "./test-results/playwright",
  fullyParallel: true,
  // A few tests assert on timer/microtask windows (the double-click monitor
  // guard, inline-expansion fills). On CI's shared 2-core runners any parallel
  // worker contention starves the CPU and stretches those windows past their
  // timeouts, so even retries fail. Run serially on CI — each test then gets a
  // full core and the windows hold — and keep retries as headroom; local dev
  // keeps full parallelism for speed.
  workers: process.env.CI ? 1 : 4,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  reporter: "line",
  use: {
    baseURL: "http://127.0.0.1:4173",
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  projects: [
    { name: "desktop", use: { ...devices["Desktop Chrome"] } },
    { name: "mobile", use: { ...devices["Pixel 7"] } },
  ],
  webServer: {
    command: "node tests/web/server.mjs",
    url: "http://127.0.0.1:4173",
    reuseExistingServer: false,
    timeout: 10000,
  },
});
