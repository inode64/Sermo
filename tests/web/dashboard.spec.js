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
  }, {
    name: "backup.mount", display_name: "Backup", category: "backup", path: "/backup",
    mounted: true, state: "active", refcount: 0, blockers: [], can_umount: true,
  }],
  notifiers: [{ name: "ops", type: "slack", enabled: true, summary: "hooks.slack.com", used_by: 2 }],
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
}, {
  name: "net-wan", display_name: "WAN", category: "network",
  enabled: true, monitored: true, state: "ok", check_type: "net",
  summary: "wan state up", interval: "30s", status_observed_at: "2026-07-10T12:00:00Z",
}, {
  name: "icmp-gateway", display_name: "Gateway", category: "network",
  enabled: true, monitored: true, state: "ok", check_type: "icmp",
  summary: "gateway reachable", interval: "30s", status_observed_at: "2026-07-10T12:00:00Z",
}, {
  name: "dns-upstream", display_name: "Upstream DNS", category: "network",
  enabled: true, monitored: true, state: "stale", sample_state: "stale", check_type: "dns",
  summary: "", interval: "1m", last_checked_at: "2026-07-10T11:57:00Z",
}, {
  name: "storage-data", display_name: "Data volume", category: "storage",
  enabled: true, monitored: true, state: "ok", check_type: "storage",
  storage: { filesystem: "ext4", mount_point: "/data", used_bytes: 10, total_bytes: 100 },
  summary: "10% used", interval: "1m", status_observed_at: "2026-07-10T12:00:00Z",
}, {
  name: "storage-backup", display_name: "Backup volume", category: "storage",
  enabled: true, monitored: true, state: "ok", check_type: "storage",
  storage: { filesystem: "xfs", mount_point: "/backup", used_bytes: 20, total_bytes: 100 },
  summary: "20% used", interval: "1m", status_observed_at: "2026-07-10T12:00:00Z",
}, {
  name: "hdparm-sda", display_name: "Disk speed", category: "storage",
  enabled: true, monitored: true, state: "ok", check_type: "hdparm", can_probe: true,
  probe: { state: "running", started_at: "2026-07-10T12:00:00Z" },
  summary: "hdparm /dev/sda", interval: "6h", status_observed_at: "2026-07-10T12:00:00Z",
}, {
  name: "smart-sda", display_name: "Disk health", category: "storage",
  enabled: true, monitored: true, state: "testing", check_type: "smart", can_probe: true,
  readings: [{ field: "device", label: "Device", value: "/dev/sda" }, { field: "device_state", label: "State", value: "testing" }],
  summary: "smart /dev/sda self-test", interval: "1d", status_observed_at: "2026-07-10T12:00:00Z",
}];

const applications = [{
  name: "nginx", display_name: "Nginx", category: "web", state: "ok",
  status: "ok", version: "1.28.0", version_short: "1.28.0",
  observed_at: "2026-07-10T12:00:00Z",
}, {
  name: "postgres", display_name: "PostgreSQL", category: "data", state: "failed",
  status: "error: exit 1", version: "16.3", version_short: "16.3",
  observed_at: "2026-07-10T12:00:00Z",
}];

const libraries = [{
  name: "openssl", display_name: "OpenSSL", category: "crypto", state: "ok",
  status: "ok", version: "OpenSSL 3.5.1", version_short: "3.5.1",
  binary: "/usr/lib64/libssl.so", observed_at: "2026-07-10T12:00:00Z",
}, {
  name: "zlib", display_name: "zlib", category: "compression", state: "warning",
  status: "version unavailable", version: "1.3.1", version_short: "1.3.1",
  binary: "/usr/lib64/libz.so", observed_at: "2026-07-10T12:00:00Z",
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
      case "/api/libraries":
        body = libraries;
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
  await expect(page.locator("#app-row-postgres")).toBeVisible();
  await expect(page.locator("#library-row-openssl")).toBeVisible();
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

test("section navigation uses two scrollable rows on compact screens", async ({ page }) => {
  const nav = page.locator("#section-nav");
  const layout = await nav.evaluate((element) => {
    const style = getComputedStyle(element);
    return {
      display: style.display,
      gridAutoFlow: style.gridAutoFlow,
      rows: style.gridTemplateRows,
      overflowX: style.overflowX,
    };
  });

  expect(layout.overflowX).toBe("auto");
  if ((page.viewportSize() || {}).width <= 1024) {
    expect(layout.display).toBe("grid");
    expect(layout.gridAutoFlow).toBe("column");
    expect(layout.rows.split(" ")).toHaveLength(2);
  } else {
    expect(layout.display).toBe("flex");
  }
});

test("single-choice filters stay hidden", async ({ page }) => {
  await expect(page.locator("#svc-category")).toBeHidden();
  await expect(page.locator("#app-category")).toBeVisible();
  await expect(page.locator("#library-category")).toBeVisible();
  await expect(page.locator("#watch-type")).toBeVisible();
});

test("inventory panels group by their meaningful type", async ({ page }) => {
  await expect(page.locator("#watch-rows .group-row")).toHaveCount(3);
  await page.locator("#mount-group-toggle").click();
  await expect(page.locator("#mount-rows .group-row")).toHaveCount(2);
  await page.locator('#watch-rows [data-group-name="Network"]').click();
  await expect(page.locator("#wat-row-icmp-gateway")).toBeHidden();
});

test("storage watches filter by filesystem and sort their own columns", async ({ page }) => {
  const filesystem = page.locator('[data-watch-type-filter="storage"]');
  await expect(filesystem).toBeVisible();
  await filesystem.selectOption("xfs");
  await expect(page.locator("#wat-row-storage-backup")).toBeVisible();
  await expect(page.locator("#wat-row-storage-data")).toBeHidden();

  await filesystem.selectOption("all");
  const usage = page.locator('[data-watch-type-sort-type="storage"][data-watch-type-sort="usage"]');
  await expect(usage).toBeVisible();
  await usage.click();
  await expect(page.locator('#watch-rows [data-watch-type-sort-type="storage"][data-watch-type-sort="usage"]')).toHaveAttribute("aria-sort", "ascending");
  await expect(page.locator('#watch-rows tr[data-exp-key^="wat:storage"]')).toHaveCount(2);
  await expect(page.locator('#watch-rows tr[data-exp-key^="wat:storage"]').first()).toHaveAttribute("id", "wat-row-storage-data");
});

test("a running manual probe keeps health visible and disables a duplicate", async ({ page }) => {
  const row = page.locator("#wat-row-hdparm-sda");
  await expect(row).toContainText("checking");
  await expect(row).toContainText("previously ok");
  const probe = row.locator('[data-watch-action="probe"]');
  await expect(probe).toBeDisabled();
  await expect(probe).toHaveAttribute("aria-describedby", "wat-hdparm-sda-probe-hint");
  await expect(page.locator("#wat-hdparm-sda-probe-hint")).toHaveText("manual probe is already running");
  await expect(row.locator("[data-probe-started-at]")).toBeVisible();
});

test("stale watch samples are visible and filterable", async ({ page }) => {
  const row = page.locator("#wat-row-dns-upstream");
  await expect(row).toContainText("stale");
  await expect(row).toHaveClass(/row-warning/);
  await expect(row.locator(".watch-sample-note")).toHaveText("stale");

  await page.locator('[data-wf="stale"]').click();
  await expect(row).toBeVisible();
  await expect(page.locator("#wat-row-net-wan")).toBeHidden();
});

test("a SMART self-test remains the device state after its start command returns", async ({ page }) => {
  const row = page.locator("#wat-row-smart-sda");
  await expect(row).toContainText("testing");
  await expect(row).not.toContainText("checking");
  await expect(row.locator(".state-testing")).toBeVisible();
});

test("global search opens a service and exposes individual actions", async ({ page }) => {
  await page.locator("#target-search").fill("service: db");
  await page.locator("#target-search").press("Enter");

  const row = page.locator("#svc-row-db");
  await expect(row).toBeVisible();
  await expect(page.locator('[data-service-detail="db"]')).toBeVisible();
  await expect(row.locator('[data-service-action="reload"]')).toBeVisible();
  await expect(row.locator('[data-service-action="unmonitor"]')).toBeVisible();
});

test("notifier test asks for confirmation and posts one named notifier", async ({ page }) => {
  await page.locator("#notifiers-section > summary").click();
  const button = page.locator('[data-notifier-test="ops"]');
  await expect(button).toBeVisible();
  const request = page.waitForRequest((req) => req.method() === "POST" && new URL(req.url()).pathname === "/api/notifiers/ops/test");
  await button.click();
  await expect(page.locator("#simple-confirm")).toBeVisible();
  await page.locator('[data-simple-result="true"]').click();
  await request;
});

test("libraries inventory is visible and searchable", async ({ page }) => {
  await expect(page.locator("#library-row-openssl")).toBeVisible();
  await page.locator("#library-search").fill("OpenSSL");
  await expect(page.locator("#library-row-openssl")).toBeVisible();
  await page.locator("#library-row-openssl .row-toggle").click();
  await expect(page.locator("#library-row-openssl")).toContainText("OpenSSL");
  await expect(page.locator("#exp-lib\\:openssl")).toContainText("/usr/lib64/libssl.so");
});

test("application and library inventories filter, group, sort, and expand", async ({ page }) => {
  await page.locator("#app-category").selectOption("data");
  await expect(page.locator("#app-row-postgres")).toBeVisible();
  await expect(page.locator("#app-row-nginx")).toBeHidden();
  await page.locator("#app-category").selectOption("all");
  await page.locator("#app-group-toggle").click();
  await expect(page.locator("#app-rows .group-row")).toHaveCount(2);
  await page.locator('[data-group-panel="app"][data-group-name="data"]').click();
  await expect(page.locator("#app-row-postgres")).toBeHidden();
  await page.locator("#app-groups-toggle").click();
  await expect(page.locator("#app-row-nginx")).toBeHidden();
  await page.locator("#app-groups-toggle").click();
  await expect(page.locator("#app-row-postgres")).toBeVisible();
  await page.locator('[data-app-sort="version"]').click();
  await expect(page.locator('[data-app-sort="version"]')).toHaveAttribute("aria-sort", "ascending");
  const postgresToggle = page.locator("#app-row-postgres .row-toggle");
  await postgresToggle.click();
  await expect(postgresToggle).toHaveAttribute("aria-expanded", "true");
  await expect(postgresToggle).toHaveAttribute("aria-controls", "exp-app:postgres");
  await expect(page.locator('[id="exp-app:postgres"]')).toContainText("16.3");

  await page.locator('[data-lf="warning"]').click();
  await expect(page.locator("#library-row-zlib")).toBeVisible();
  await expect(page.locator("#library-row-openssl")).toBeHidden();
  await page.locator('[data-lf="all"]').click();
  await page.locator("#library-group-toggle").click();
  await expect(page.locator("#library-rows .group-row")).toHaveCount(2);
  await page.locator('[data-group-panel="library"][data-group-name="compression"]').click();
  await expect(page.locator("#library-row-zlib")).toBeHidden();
  await page.locator("#library-groups-toggle").click();
  await expect(page.locator("#library-row-openssl")).toBeHidden();
  await page.locator("#library-groups-toggle").click();
  await expect(page.locator("#library-row-zlib")).toBeVisible();
  await page.locator('[data-library-sort="version"]').click();
  await expect(page.locator('[data-library-sort="version"]')).toHaveAttribute("aria-sort", "ascending");
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
