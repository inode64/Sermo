const { defineConfig, devices } = require("@playwright/test");

module.exports = defineConfig({
  testDir: "./tests/web",
  outputDir: "./test-results/playwright",
  fullyParallel: true,
  // CI runners are 2-core; 4 workers oversubscribe them, and the resulting CPU
  // starvation stretches timer/microtask-driven assertions (expansion fills,
  // click-guard windows) past their timeouts, flaking under load. Match workers
  // to the runner's cores there and keep retry headroom; local dev keeps 4.
  workers: process.env.CI ? 2 : 4,
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
