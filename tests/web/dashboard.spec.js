const { test, expect } = require("@playwright/test");
const AxeBuilder = require("@axe-core/playwright").default;

const services = [
  {
    name: "web", display_name: "Web server", category: "service", enabled: true,
    monitored: true, status: "active", state: "started", can_reload: true,
    uptime_seconds: 7200, status_observed_at: "2026-07-10T12:00:00Z",
  },
  {
    name: "db", display_name: "Database", category: "service", enabled: true,
    monitored: true, status: "active", state: "started", can_reload: true,
    uptime_seconds: 10800, status_observed_at: "2026-07-10T12:00:00Z",
  },
];

const dashboard = {
  generated_at: "2026-07-10T12:00:00Z",
  services,
  mounts: [{
    name: "data.mount", display_name: "Data", category: "storage", path: "/data",
    mounted: true, state: "active", refcount: 1, blockers: [], can_umount: true,
  }],
  notifiers: [],
  daemon: { backend: "systemd", hostname: "fixture", host_uptime_seconds: 86400 },
  daemon_metrics: null,
  locks: [],
  activity: { total_events: 1, last_event_kind: "action", last_event_time: "2026-07-10T12:00:00Z" },
  ready: { ready: true, status: "ok", backend: "systemd", services: 2, watches: 1 },
  live: { status: "ok", uptime: "1h", uptime_seconds: 3600, services: 2, go: "go1.test" },
  monitoring: { monitored: 2, paused: 0, total: 2 },
  operations: { in_use: 0, total: 4, active_users: 1 },
  host_metrics: [],
};

const watches = [{
  name: "process-queue", display_name: "Process queue", category: "watch",
  enabled: true, monitored: true, state: "ok", check_type: "process",
  summary: "2 processes", interval: "1m", status_observed_at: "2026-07-10T12:00:00Z",
}];

const applications = [{
  name: "nginx", display_name: "Nginx", category: "web", state: "ok",
  status: "ok", version: "1.28.0", version_short: "1.28.0",
  observed_at: "2026-07-10T12:00:00Z",
}];

function serviceDetail(name) {
  const service = services.find((item) => item.name === name);
  return {
    ...service,
    unit: `${name}.service`,
    interval: "30s",
    checks: [{ name: "latency", type: "http", ran: true, ok: true, message: "status 200", sla: [] }],
    processes: [{ pid: name === "web" ? 101 : 202, cmdline: [name], user: "root", role: "main", rss: 1048576 }],
    process_totals: { count: 1, rss: 1048576, io_read: 0, io_write: 0, fds: 5, threads: 1 },
    locks: [], rules: [], sla: [],
  };
}

async function mockAPI(page) {
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    let body;
    switch (path) {
      case "/api/whoami":
        body = { can_act: true, role: "admin", auth: false };
        break;
      case "/api/dashboard":
        body = dashboard;
        break;
      case "/api/watches":
        body = watches;
        break;
      case "/api/applications":
        body = applications;
        break;
      case "/api/events":
        body = {
          events: [{ id: 1, time: "2026-07-10T12:00:00Z", service: "web", kind: "action", status: "ok", message: "started" }],
          has_more: false,
        };
        break;
      default: {
        const detailMatch = path.match(/^\/api\/services\/([^/]+)$/);
        const eventsMatch = path.match(/^\/api\/services\/([^/]+)\/events$/);
        if (detailMatch) body = serviceDetail(decodeURIComponent(detailMatch[1]));
        else if (eventsMatch) body = [];
        else if (path.endsWith("/sla")) body = { since: url.searchParams.get("since"), points: [] };
        else if (path.endsWith("/metrics")) body = { summary: {}, points: [], unit: "ms" };
        else if (path.endsWith("/runtime")) body = { cpu: { points: [], unit: "%" }, memory: { points: [], unit: "bytes" }, io: { points: [], unit: "B/s" } };
        else body = {};
      }
    }
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
  });
}

test.beforeEach(async ({ page }) => {
  await mockAPI(page);
  await page.goto("/");
  await expect(page.locator("#svc-row-web")).toBeVisible();
});

test("dashboard passes axe and fits the viewport", async ({ page }) => {
  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag22aa"])
    .analyze();
  expect(results.violations).toEqual([]);

  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(overflow).toBeLessThanOrEqual(1);
  await expect(page.locator("#target-search")).toBeVisible();
});

test("global search opens a service and exposes compact actions", async ({ page }) => {
  await page.locator("#target-search").fill("service: db");
  await page.locator("#target-search").press("Enter");

  const row = page.locator("#svc-row-db");
  await expect(row).toBeVisible();
  await expect(page.locator('[data-service-detail="db"]')).toBeVisible();
  await row.locator(".row-action-menu > summary").click();
  await expect(row.locator('[data-service-action="reload"]')).toBeVisible();
});

test("graph selections remain isolated per service", async ({ page }) => {
  for (const name of ["web", "db"]) {
    await page.locator("#target-search").fill(`service: ${name}`);
    await page.locator("#target-search").press("Enter");
    await expect(page.locator(`[data-service-detail="${name}"]`)).toBeVisible();
  }

  const webDetail = page.locator('[data-service-detail="web"]');
  const dbDetail = page.locator('[data-service-detail="db"]');
  await webDetail.locator('[data-window-kind="setMetricWin"][data-window-value="1h"]').click();
  await dbDetail.locator('[data-window-kind="setMetricWin"][data-window-value="168h"]').click();

  await expect(webDetail.locator('[data-window-value="1h"]')).toHaveAttribute("aria-pressed", "true");
  await expect(dbDetail.locator('[data-window-value="168h"]')).toHaveAttribute("aria-pressed", "true");
  const saved = await page.evaluate(() => JSON.parse(localStorage.getItem("sermo-ui-state")));
  expect(saved.serviceMetricStates.web.window).toBe("1h");
  expect(saved.serviceMetricStates.db.window).toBe("168h");
});
