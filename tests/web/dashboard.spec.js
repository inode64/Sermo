const { test, expect } = require("@playwright/test");
const AxeBuilder = require("@axe-core/playwright").default;

const delay = (milliseconds) => new Promise((resolve) => {
  setTimeout(resolve, milliseconds);
});

const services = [
  {
    name: "web", display_name: "Web server", category: "service", enabled: true,
    monitored: true, status: "active", state: "active", can_reload: true,
    uptime_seconds: 7200, status_observed_at: "2026-07-10T12:00:00Z",
  },
  {
    name: "db", display_name: "Database", category: "service", enabled: true,
    monitored: true, status: "active", state: "started", can_reload: true,
    uptime_seconds: 10800, status_observed_at: "2026-07-10T12:00:00Z",
    last_event: { time: "2026-07-10T11:59:00Z", kind: "reload", message: "config reloaded" },
  },
  {
    name: "stale", display_name: "Stale binary", category: "service", enabled: true,
    monitored: true, status: "active", state: "warning", warning_reason: "stale_binary",
    uptime_seconds: 3600, status_observed_at: "2026-07-10T12:00:00Z", strays: 3,
  },
];

const dashboard = {
  generation: 7,
  services,
  sessions: {
    sources: [
      { kind: "ssh", service: "web", state: "available" },
      { kind: "tmux", service: "web", check: "tmux-root", user: "root", state: "available" },
      { kind: "tmux", service: "web", check: "tmux-empty", user: "root", state: "available", can_close_empty: true },
      { kind: "screen", service: "web", check: "screen-root", user: "root", state: "available" },
    ],
    ssh: [{ service: "web", user: "root", terminal: "pts/11", pid: 96, start_ticks: 1234, idle_seconds: 120, can_close: true, memory_ready: true, rss: 1048576, cpu_ready: true, cpu: 1.5, io_ready: true, io_read: 1000, io_write: 250 }],
    terminal: [
      { service: "web", check: "tmux-root", multiplexer: "tmux", user: "root", name: "ops", state: "attached", windows: 2, idle_seconds: 300, has_idle: true, memory_ready: true, rss: 2097152, cpu_ready: true, cpu: 2.5, io_ready: true, io_read: 2000, io_write: 500, identity: "$7:90", can_close: true },
      { service: "web", check: "tmux-root", multiplexer: "tmux", user: "root", name: "build", state: "detached", windows: 1, idle_seconds: 60, has_idle: true, memory_ready: true, rss: 524288, cpu_ready: true, cpu: 0, io_ready: true, io_read: 0, io_write: 0, identity: "$8:91", can_close: true },
    ],
  },
  mounts: [{
    name: "data.mount", display_name: "Data", category: "storage", path: "/data",
    mounted: true, state: "active", refcount: 1, blockers: [], can_umount: true,
  }, {
    name: "backup.mount", display_name: "Backup", category: "backup", path: "/backup",
    mounted: true, state: "active", refcount: 0, blockers: [], can_umount: true,
  }],
  notifiers: [{ name: "ops", type: "slack", enabled: true, summary: "hooks.slack.com", used_by: 2 }],
  daemon: { backend: "systemd", hostname: "fixture", host_uptime_seconds: 86400, active_users: 1, sessions: { console: 0, ssh: 3 } },
  daemon_metrics: {
    current: { pid: 4242, fds: 12345, threads: 8, cpu_ready: true, cpu: 1.5, rss: 1048576, io_ready: true, io: 2048 },
  },
  locks: [],
  activity: { errors: 0, last_event_kind: "action" },
  ready: { ready: true, status: "ok", backend: "systemd", services: 2, watches: 1 },
  live: { status: "ok", uptime: "1h", uptime_seconds: 3600, services: 2, go: "go1.test" },
  monitoring: { monitored: 2, paused: 0, total: 2 },
  host_metrics: [],
};

const watches = [{
  name: "process-queue", display_name: "Process queue", category: "watch",
  scope: "service",
  enabled: true, monitored: true, state: "ok", check_type: "process",
  summary: "2 processes", interval: "1m", status_observed_at: "2026-07-10T12:00:00Z",
}, {
  name: "net-wan", display_name: "WAN", category: "network",
  enabled: true, monitored: true, state: "ok", check_type: "net", keeps_sla: true,
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
  name: "firewall-paused", display_name: "Firewall", category: "network",
  enabled: true, monitored: false, state: "disabled", monitor: "previous", monitor_source: "web",
  monitor_changed_at: "2026-07-10T11:55:00Z", check_type: "firewall", interval: "1m",
  last_checked_at: "2026-07-10T11:54:00Z",
}, {
  name: "legacy-watch", display_name: "Legacy watch", category: "watch",
  enabled: false, monitored: false, state: "disabled", check_type: "process", interval: "1m",
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
  name: "hdparm-sdd", display_name: "Backup disk speed", category: "storage",
  enabled: true, monitored: true, state: "warning", check_type: "hdparm",
  readings: [{ field: "warning", label: "Warning", warning: "hdparm /dev/sdd read=0.4 MB/s" }],
  summary: "hdparm /dev/sdd read=0.4 MB/s", interval: "6h", status_observed_at: "2026-07-10T12:00:00Z",
}, {
  name: "diskio-sdd", display_name: "Backup disk I/O", category: "storage",
  enabled: true, monitored: true, state: "ok", check_type: "diskio",
  readings: [
    { field: "device", label: "Device", value: "sdd" },
    { field: "bus", label: "Bus", value: "usb" },
    { field: "util_pct", label: "Utilization", value: "0%" },
    { field: "read_bytes", label: "Read", value: "0 B/s" },
    { field: "write_bytes", label: "Write", value: "0 B/s" },
    { field: "await_ms", label: "Await", value: "0.0 ms" },
    { field: "read_total_bytes", label: "Read total", value: "12.8 GB" },
    { field: "write_total_bytes", label: "Written total", value: "57 KB" },
  ],
  summary: "diskio sdd idle", interval: "30s", status_observed_at: "2026-07-10T12:00:00Z",
}, {
  name: "smart-sda", display_name: "Disk health", category: "storage",
  enabled: true, monitored: true, state: "testing", check_type: "smart", can_probe: true,
  readings: [{ field: "device", label: "Device", value: "/dev/sda" }, { field: "device_state", label: "State", value: "testing" }],
  summary: "smart /dev/sda self-test", interval: "1d", status_observed_at: "2026-07-10T12:00:00Z",
}, {
  name: "smart-sdb", display_name: "Healthy disk health", category: "storage",
  enabled: true, monitored: true, state: "ok", check_type: "smart", can_probe: true,
  readings: [
    { field: "device", label: "Device", value: "/dev/sdb" },
    { field: "health", label: "Health", value: "PASSED" },
    { field: "temperature", label: "temperature", value: "42 °C" },
  ],
  summary: "smart /dev/sdb health=PASSED", interval: "1d", status_observed_at: "2026-07-10T12:00:00Z",
}, {
  name: "smart-sdc", display_name: "Verdictless disk health", category: "storage",
  enabled: true, monitored: true, state: "ok", check_type: "smart", can_probe: true,
  readings: [
    { field: "device", label: "Device", value: "/dev/sdc" },
    { field: "health", label: "Health", value: "unknown" },
  ],
  summary: "smart /dev/sdc health=unknown", interval: "1d", status_observed_at: "2026-07-10T12:00:00Z",
}, {
  name: "smart-sdz", display_name: "Dead disk health", category: "storage",
  enabled: true, monitored: true, state: "missing", check_type: "smart", can_probe: true,
  readings: [
    { field: "error", label: "Error", error: "smart /dev/sdz: device missing" },
    { field: "device", label: "Device", value: "/dev/sdz" },
    { field: "device_state", label: "State", value: "missing" },
    { field: "health", label: "Health", value: "missing" },
  ],
  summary: "smart /dev/sdz: device missing", interval: "1d", status_observed_at: "2026-07-10T12:00:00Z",
}];

const applications = [{
  name: "nginx", display_name: "Nginx", category: "web", state: "ok",
  status: "ok", version: "1.28.0", version_short: "1.28.0",
  observed_at: "2026-07-10T12:00:00Z", keeps_sla: true,
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

const dashboardFallbackFields = Object.freeze({
  "/api/services": "services",
  "/api/sessions": "sessions",
  "/api/mounts": "mounts",
  "/api/notifiers": "notifiers",
  "/api/daemon": "daemon",
  "/api/daemon/metrics": "daemon_metrics",
  "/api/locks": "locks",
  "/api/activity": "activity",
  "/api/monitoring": "monitoring",
  "/api/host": "host_metrics",
});

function serviceDetail(name) {
  const service = services.find((item) => item.name === name);
  const namedMetrics = [{ name: "users", type: "users", ran: true, ok: true, message: "2 users", metrics: [{ name: "count", unit: "users" }] }];
  return {
    ...service,
    unit: `${name}.service`,
    interval: "30s",
    checks: [{ name: "latency", type: "http", ran: true, ok: true, message: "status 200" }, ...namedMetrics],
    processes: [{
      pid: name === "web" ? 101 : 202, cmdline: [name], user: "root", role: "main", rss: 1048576,
      // 96.25% spread over threads, of which the busiest held 61.5% of one core:
      // max_core must be readable as its own figure, not confused with cpu.
      has_cpu: true, cpu: 96.25, threads: 4, max_core: 61.5, max_core_exact: true,
    }],
    process_totals: {
      count: 1, rss: 1048576, io_read: 0, io_write: 0, fds: 5, threads: 1,
      has_cpu: true, cpu: 12.5, cpu_thread: 96.25, num_cpu: 4,
    },
    locks: [], rules: [], sla: [],
  };
}

async function mockAPI(page) {
  await page.route("**/readyz**", (route) => route.fulfill({ json: dashboard.ready }));
  await page.route("**/livez**", (route) => route.fulfill({ json: dashboard.live }));
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
      case "/api/stream":
        await route.fulfill({
          status: 200,
          contentType: "text/event-stream",
          body: "retry: 60000\n\n",
        });
        return;
      case "/api/events":
		body = {
		  events: [{ id: 1, time: "2026-07-10T12:00:00Z", service: "web", kind: "action", status: "ok", message: "started" }],
		  has_more: false,
		};
        break;
      default: {
        const dashboardField = dashboardFallbackFields[path];
        const detailMatch = path.match(/^\/api\/services\/([^/]+)$/);
        const eventsMatch = path.match(/^\/api\/services\/([^/]+)\/events$/);
        if (dashboardField) body = dashboard[dashboardField];
        else if (detailMatch) body = serviceDetail(decodeURIComponent(detailMatch[1]));
        else if (eventsMatch) body = [];
        else if (path.endsWith("/sla")) {
          if (path.startsWith("/api/services/web/") || path.startsWith("/api/services/nginx/")
            || path.startsWith("/api/watches/net-wan/")) {
            const now = Date.now();
            // A check's series is scoped with ?check= and is deliberately
            // distinct from the service's, so the strip cannot be passing by
            // reading the service series for a check.
            const points = url.searchParams.get("check")
              ? [{ start: new Date(now - 10 * 60 * 1000).toISOString(), up: 40, total: 40, down_buckets: 0 }]
              : [
                { start: new Date(now - 30 * 60 * 1000).toISOString(), up: 60, total: 60, down_buckets: 0 },
                { start: new Date(now - 5 * 60 * 1000).toISOString(), up: 30, total: 60, down_buckets: 1 },
              ];
            body = { since: url.searchParams.get("since"), points };
          } else body = { since: url.searchParams.get("since"), points: [] };
        }
        else if (path.endsWith("/metrics")) {
          if (url.searchParams.get("metric") === "count") {
            if (path.startsWith("/api/services/db/")) {
              await route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ message: "metric store unavailable" }) });
              return;
            }
            body = {
              check: url.searchParams.get("check"), metric: "count", unit: "users",
              summary: { count: 1, avg: 2, min: 2, max: 2 },
              points: [{ start: new Date().toISOString(), n: 1, avg: 2, min: 2, max: 2 }],
            };
          } else body = { summary: {}, points: [], unit: "ms" };
        }
        else if (path.endsWith("/runtime")) body = { cpu: { points: [], unit: "%" }, memory: { points: [], unit: "bytes" }, io: { points: [], unit: "B/s" } };
        else body = {};
      }
    }
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
  });
}

const uncaughtPageErrors = new WeakMap();

test.beforeEach(async ({ page }) => {
  const errors = [];
  uncaughtPageErrors.set(page, errors);
  page.on("pageerror", (error) => errors.push(error.message));
  await mockAPI(page);
  await page.goto("/");
  await expect(page.locator("#svc-row-web")).toBeVisible();
  await expect(page.locator("#app-row-postgres")).toBeVisible();
  await expect(page.locator("#library-row-openssl")).toBeVisible();
});

test.afterEach(async ({ page }) => {
  expect(uncaughtPageErrors.get(page) || [], "uncaught browser errors").toEqual([]);
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

// Multi-host operators keep many tabs open; the document title must name the
// short host from GET /api/daemon (fixture here) so tabs stay distinguishable.
// Attention signals prefix "(N) "; healthy hosts use "Sermo - <host>" alone.
test("browser tab title includes the short hostname", async ({ page }) => {
  await expect(page).toHaveTitle(/Sermo - fixture/);
});

test("header separates console and SSH sessions", async ({ page }) => {
  await expect(page.locator("#statusbar")).toContainText("sessions (console/SSH): 0/3");
});

test("overview host tiles share percentage, capacity and severity rendering", async ({ page }) => {
  const body = JSON.parse(JSON.stringify(dashboard));
  body.host_metrics = [
    { name: "total_cpu", percent: 12.5 },
    { name: "total_memory", absolute: 536870912, total: 1073741824, percent: 50 },
    { name: "total_swap", absolute: 805306368, total: 1073741824, percent: 75 },
    { name: "load1", absolute: 3.25, total: 2, percent: 162.5 },
  ];
  await page.route("**/api/dashboard**", (route) => route.fulfill({ json: body }));
  await page.locator("#refresh-now").click();

  const cpu = page.locator("#overview .tile", { hasText: "Host CPU" });
  const memory = page.locator("#overview .tile", { hasText: "Host memory" });
  const swap = page.locator("#overview .tile", { hasText: "Host swap" });
  const load = page.locator("#overview .tile", { hasText: "Load 1m" });
  await expect(cpu).toContainText("12.5%");
  await expect(memory).toContainText("50%");
  await expect(memory).toContainText("512 MiB used · 512 MiB free");
  await expect(swap).toContainText("75%");
  await expect(swap).toHaveClass(/t-warn/);
  await expect(load).toContainText("3.25");
  await expect(load).toContainText("2 CPUs · 162.5%");
  await expect(load).toHaveClass(/t-crit/);
  await expect(page.locator("#tile-cpu-gauge, #tile-mem-gauge, #tile-swap-gauge, #tile-load-gauge")).toHaveCount(4);
});

test("section navigation wraps instead of scrolling sideways on compact screens", async ({ page }) => {
  const nav = page.locator("#section-nav");
  const layout = await nav.evaluate((element) => {
    const style = getComputedStyle(element);
    return {
      display: style.display,
      flexWrap: style.flexWrap,
      overflowX: style.overflowX,
      fits: element.scrollWidth <= element.clientWidth + 1,
    };
  });

  expect(layout.display).toBe("flex");
  if ((page.viewportSize() || {}).width <= 1024) {
    // Every section pill wraps into view so no sideways swipe is needed.
    expect(layout.flexWrap).toBe("wrap");
    expect(layout.overflowX).toBe("visible");
    expect(layout.fits).toBe(true);
  } else {
    expect(layout.overflowX).toBe("auto");
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
  await expect(page.locator("#wat-row-process-queue .watch-scope")).toHaveText("service");
  // Host scope is the panel default and is not repeated after every name.
  await expect(page.locator("#wat-row-storage-data .watch-scope")).toHaveCount(0);
  await page.locator("#mount-group-toggle").click();
  await expect(page.locator("#mount-rows .group-row")).toHaveCount(2);
  await page.locator('#watch-rows [data-group-name="Network"]').click();
  await expect(page.locator("#wat-row-icmp-gateway")).toBeHidden();
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

test("an advisory watch reads amber and never red", async ({ page }) => {
  const row = page.locator("#wat-row-hdparm-sdd");
  await expect(row).toHaveClass(/row-warning/);
  // The whole point of the severity grade: a warning must never borrow the
  // colour an outage owns.
  await expect(row).not.toHaveClass(/row-failing/);
  await expect(row.locator(".state-warning")).toHaveText("warning");

  await page.locator('[data-wf="warning"]').click();
  await expect(row).toBeVisible();
  await expect(page.locator("#wat-row-hdparm-sda")).toBeHidden();
});

test("an idle disk still reports what it has ever moved", async ({ page }) => {
  const row = page.locator("#wat-row-diskio-sdd");
  // The rate columns are all zero — that is honest, and on its own it is also
  // indistinguishable from a disk nothing ever touches.
  await expect(row).toContainText("0 B/s");
  // The cumulative totals are what tell the two apart.
  await expect(row).toContainText("12.8 GB");
  await expect(row).toContainText("57 KB");
  // And the bus explains why this one sleeps in the first place.
  await expect(row).toContainText("usb");
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

test("stale binary keeps its warning state without a restart hint", async ({ page }) => {
  const row = page.locator("#svc-row-stale");
  const state = row.locator("td").nth(2);
  await expect(state).toHaveText("warning");
  await expect(state.locator(".state-reason")).toHaveCount(0);
  await expect(state).not.toContainText("binary replaced on disk");

  await row.locator(".row-toggle").click();
  const detail = page.locator('[data-service-detail="stale"]');
  await expect(detail.locator(".runtime-grid .state-reason")).toHaveCount(0);
  await expect(detail).not.toContainText("binary replaced on disk");
});

test("paused monitoring is distinct from disabled configuration", async ({ page }) => {
  const paused = page.locator("#wat-row-firewall-paused");
  await expect(paused.locator(".target-state")).toHaveText("monitoring paused");
  await expect(paused).toContainText("via web UI");

  await paused.locator(".row-toggle").click();
  const detail = page.locator('[id="exp-wat:firewall-paused"]');
  await expect(detail).toContainText("Monitoring");
  await expect(detail).toContainText("Configured monitor");
  await expect(detail).toContainText("previous");
  await expect(detail).toContainText("Last checked");

  const disabled = page.locator("#wat-row-legacy-watch");
  await expect(disabled.locator(".target-state")).toHaveText("disabled in config");

  await page.locator('[data-wf="disabled"]').click();
  await expect(paused).toBeVisible();
  await expect(disabled).toBeVisible();
  await expect(page.locator("#wat-row-net-wan")).toBeHidden();
});

test("a SMART self-test remains the device state after its start command returns", async ({ page }) => {
  const row = page.locator("#wat-row-smart-sda");
  await expect(row).toContainText("testing");
  await expect(row).not.toContainText("checking");
  await expect(row.locator(".state-testing")).toBeVisible();
});

test("SMART health is read in smartctl's own words, so a PASSED drive is not painted as failing", async ({ page }) => {
  const row = page.locator("#wat-row-smart-sdb");
  // The Health column is one of the ones the tablet-width rules in styles.css
  // hide, so assert the class the cell maps its verdict to rather than its
  // visibility: the colour is the point, and it is what a wide viewport shows.
  await expect(row.locator(".ok")).toHaveText("PASSED");
  await expect(row.locator(".bad")).toHaveCount(0);

  // A drive that answered without a verdict is not a failing drive: it takes the
  // same warning colour an unknown backend status does.
  const verdictless = page.locator("#wat-row-smart-sdc");
  await expect(verdictless.locator(".unknown")).toHaveText("unknown");
  await expect(verdictless.locator(".bad")).toHaveCount(0);
});

test("a device that stopped answering reads as missing, not as a blank health cell", async ({ page }) => {
  const row = page.locator("#wat-row-smart-sdz");
  await expect(row.locator(".state-missing")).toBeVisible();
  await expect(row.locator(".state-missing")).toHaveText("missing");
  // The health column must name the fault instead of falling back to the em
  // dash a check with no reading shows. The temperature, wear and power-on
  // columns keep theirs: a dead drive genuinely reports no numbers.
  await expect(row.locator(".bad")).toHaveText("missing");
  // A missing device is a failure, so the row carries the same highlight a
  // failing watch does.
  await expect(row).toHaveClass(/row-failing/);
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

test("failed services prioritize restart and keep repair as a manual fallback", async ({ page }) => {
  await page.route("**/api/services/failed-repair", async (route) => {
    await route.fulfill({ json: {
      name: "failed-repair", display_name: "Failed repair", category: "service", enabled: true,
      monitored: true, status: "failed", state: "failed", can_reload: false,
      also_apply: ["db"], unit: "failed-repair.service", checks: [], processes: [], locks: [], rules: [], sla: [],
    } });
  });
  await page.route("**/api/dashboard**", async (route) => {
    const body = JSON.parse(JSON.stringify(dashboard));
    body.services.push({
      name: "failed-repair", display_name: "Failed repair", category: "service", enabled: true,
      monitored: true, status: "failed", state: "failed", can_reload: false,
      also_apply: ["db"],
      status_observed_at: "2026-07-10T12:00:00Z",
    });
    await route.fulfill({ json: body });
  });
  await page.reload();

  const row = page.locator("#svc-row-failed-repair");
  const actions = row.locator("[data-service-action]");
  const rendered = await actions.evaluateAll((buttons) => buttons.map((button) => button.dataset.serviceAction));
  expect(rendered[0]).toBe("restart");
  expect(rendered.filter((action) => action === "restart")).toHaveLength(1);
  expect(rendered[rendered.length - 1]).toBe("repair");

  const repair = row.locator('[data-service-action="repair"]');
  await expect(repair).toHaveAttribute("aria-label", "Repair residual service state Failed repair");
  await repair.click();
  await expect(page.locator("#action-confirm")).toBeVisible();
  await expect(page.locator("#confirm-body")).toContainText("manual recovery for a failed or inactive service");
  await expect(page.locator("#confirm-no-cascade-wrap")).toBeHidden();
  await page.keyboard.press("Escape");
});

test("service SLA renders a status-page bar strip with incidents", async ({ page }) => {
  await page.locator("#svc-row-web .row-toggle").click();
  const detail = page.locator('[data-service-detail="web"]');
  const strip = detail.locator(".sla-chart-panel .sla-bars");
  await expect(strip).toBeVisible();
  await expect(strip.locator(".sla-bar-seg")).toHaveCount(90);
  await expect(strip.locator(".sla-bar-seg:not(.sla-gap)")).toHaveCount(2);
  await expect(detail.locator(".sla-bars-axis")).toContainText("now");
  await expect(detail.locator(".sla-incident-list")).toContainText("Incidents");
});

// A check's availability comes from the same endpoint and window as the service
// timeline, and renders the same band. The band is what keeps unobserved time
// visible: the previous per-check strip collapsed a barely-measured window into
// a flat 100% bar, which read as a fully measured window.
test("check SLA uses the service series endpoint and hatches unobserved time", async ({ page }) => {
  const checkRequest = page.waitForRequest((request) => {
    const url = new URL(request.url());
    return url.pathname === "/api/services/web/sla" && url.searchParams.get("check") === "latency";
  });
  await page.locator("#svc-row-web .row-toggle").click();
  await checkRequest;

  const cell = page.locator('[data-service-detail="web"] .sla-check-strip').first();
  const bars = cell.locator(".sla-bar-seg");
  await expect(bars).toHaveCount(90);
  const serviceBand = page.locator('[data-service-detail="web"] .sla-chart-panel .sla-bars');
  await expect(serviceBand).toBeVisible();
  const checkBandHeight = await cell.locator(".sla-bars").evaluate((band) => getComputedStyle(band).height);
  const serviceBandHeight = await serviceBand.evaluate((band) => getComputedStyle(band).height);
  const checkCellMinWidth = await cell.evaluate((strip) => getComputedStyle(strip.parentElement).minWidth);
  const expectedCheckCellMinWidth = await cell.evaluate((strip) => {
    const rem = parseFloat(getComputedStyle(document.documentElement).fontSize);
    return Math.min(32 * rem, strip.ownerDocument.defaultView.innerWidth * 0.45);
  });
  expect(parseFloat(checkBandHeight)).toBeCloseTo(parseFloat(serviceBandHeight), 1);
  expect(parseFloat(checkCellMinWidth)).toBeCloseTo(expectedCheckCellMinWidth, 1);
  // The mock's single check sample sits 10 minutes into a 24h window, so every
  // bar before it must stay hatched rather than inherit the window's ratio.
  expect(await cell.locator(".sla-bar-seg.sla-gap").count()).toBeGreaterThan(0);
  await expect(cell.locator(".sla-count")).toHaveText("40/40");
});

// An application and a host watch draw availability with the service's own
// panel: the same band, the same 1h..1y selector, the same request protocol.
// Only the series differs — an application reads the service it maps to, a watch
// reads its own — which is what a separate, hand-rolled band used to obscure.
test("application and watch availability reuse the service SLA panel", async ({ page }) => {
  const appSeries = page.waitForRequest((request) => new URL(request.url()).pathname === "/api/services/nginx/sla");
  await page.locator("#app-row-nginx .row-toggle").click();
  await appSeries;
  const appPanel = page.locator('[id="exp-app:nginx"]');
  await expect(appPanel.locator("h2")).toContainText("Availability");
  await expect(appPanel.locator(".sla-chart-panel .sla-bar-seg")).toHaveCount(90);
  await expect(appPanel.locator('[data-window-kind="setSLAWin"]')).toHaveCount(5);

  // The selector refetches on the window it names, exactly as a service's does.
  const weekSeries = page.waitForRequest((request) => {
    const url = new URL(request.url());
    return url.pathname === "/api/services/nginx/sla" && url.searchParams.get("since") === "168h";
  });
  await appPanel.locator('[data-window-kind="setSLAWin"][data-window-value="168h"]').click();
  await weekSeries;

  const watchSeries = page.waitForRequest((request) => new URL(request.url()).pathname === "/api/watches/net-wan/sla");
  await page.locator("#wat-row-net-wan .row-toggle").click();
  await watchSeries;
  await expect(page.locator('[id="exp-wat:net-wan"] .sla-chart-panel .sla-bar-seg')).toHaveCount(90);

  // An application behind no monitored service has no availability to show.
  await page.locator("#app-row-postgres .row-toggle").click();
  await expect(page.locator('[id="exp-app:postgres"] .sla-chart-panel')).toHaveCount(0);
});

test("service detail graphs named check metrics and reports fetch failures", async ({ page }) => {
  const namedMetricRequest = page.waitForRequest((request) => {
    const url = new URL(request.url());
    return url.pathname === "/api/services/web/metrics"
      && url.searchParams.get("check") === "users"
      && url.searchParams.get("metric") === "count";
  });
  await page.locator("#svc-row-web .row-toggle").click();
  await namedMetricRequest;
  const webMetric = page.locator('[data-service-metric-check="users"][data-service-metric-name="count"]').first();
  await expect(webMetric).toContainText("users · count");
  await expect(webMetric).toContainText("avg 2 users");
  await expect(webMetric.locator("svg")).toBeVisible();

  const failedMetricRequest = page.waitForRequest((request) => {
    const url = new URL(request.url());
    return url.pathname === "/api/services/db/metrics"
      && url.searchParams.get("check") === "users"
      && url.searchParams.get("metric") === "count";
  });
  await page.locator("#svc-row-db .row-toggle").click();
  await failedMetricRequest;
  const dbMetric = page.locator('[data-service-detail="db"] [data-service-metric-check="users"][data-service-metric-name="count"]');
  await expect(dbMetric).toContainText("Failed to load users · count: HTTP 500");
});

test("the process table reports each process's busiest core beside its total", async ({ page }) => {
  await page.locator("#svc-row-web .row-toggle").click();
  const detail = page.locator('[data-service-detail="web"]');
  const table = detail.getByRole("table", { name: "Service processes" });

  // Max core sits immediately after CPU: the two answer different questions, and a
  // process spread over eight cores reports the same total as one pegging a single
  // core. Process CPU is already single-core normalized, so each gets one column,
  // not a percentage column plus an identical bar column.
  const headers = table.locator("thead th");
  await expect(headers).toHaveCount(10);
  await expect(headers.nth(4)).toHaveText("CPU");
  await expect(headers.nth(5)).toHaveText("Max core");

  // 96.25% total, busiest thread 61.5% of one core: the row must show both, so the
  // busiest-thread figure can never be read as the process total.
  const cells = table.locator("tbody tr").first().locator("td");
  await expect(cells.nth(4)).toContainText("96.25%");
  await expect(cells.nth(5)).toContainText("61.5%");
  // Measured, not bounded: the distinction lives in the tooltip, since a marker on the
  // cell would sit on every row of an idle host and so distinguish nothing.
  await expect(cells.nth(5).locator("[title]").first())
    .toHaveAttribute("title", /busiest thread/);

  // The aggregate no longer restates it in the General data grid: it would hide
  // which process the peak belongs to.
  await expect(detail.locator(".runtime-grid")).not.toContainText("core peak");
});

test("sessions panel shows metrics, sorts columns and closes verified SSH and tmux", async ({ page }) => {
  let closeRequest = null;
  let tmuxCloseRequest = null;
  await page.route("**/api/services/web/sessions/96/close**", async (route) => {
    closeRequest = new URL(route.request().url());
    await route.fulfill({ json: { ok: true, message: "close SSH session ok" } });
  });
  await page.route("**/api/services/web/terminal-sessions/tmux-root/close**", async (route) => {
    tmuxCloseRequest = new URL(route.request().url());
    await route.fulfill({ json: { ok: true, message: "close terminal session ok" } });
  });

  const sessions = page.getByRole("table", { name: "Current SSH, tmux and screen sessions" });
  await expect(sessions).toContainText("root");
  await expect(sessions).toContainText("pts/11");
  await expect(sessions).toContainText("120s");
  await expect(sessions).toContainText("1 MiB");
  await expect(sessions).toContainText("1 KB/s / 250 B/s");
  await expect(sessions).toContainText("ops");
  await expect(sessions).toContainText("screen-root");
  await expect(sessions).toContainText("No active sessions");

  await sessions.locator('[data-ssh-session-close]').click();
  await expect(page.locator("#simple-confirm")).toBeVisible();
  await page.locator("#simple-confirm-ok").click();
  await expect.poll(() => closeRequest && closeRequest.searchParams.get("terminal")).toBe("pts/11");
  expect(closeRequest.searchParams.get("start_ticks")).toBe("1234");

  await page.locator('[data-sf="tmux"]').click();
  await sessions.getByRole("columnheader", { name: /Idle/ }).click();
  await expect(sessions.locator("tbody tr").first()).toContainText("build");
  await sessions.locator('[data-terminal-session="ops"]').click();
  await expect(page.locator("#simple-confirm")).toBeVisible();
  await page.locator("#simple-confirm-ok").click();
  await expect.poll(() => tmuxCloseRequest && tmuxCloseRequest.searchParams.get("session")).toBe("ops");
  expect(tmuxCloseRequest.searchParams.get("identity")).toBe("$7:90");
});

test("empty tmux sources use a red state and close the server through the API", async ({ page }) => {
  let emptyCloseRequest = null;
  await page.route("**/api/services/web/terminal-sessions/tmux-empty/close-empty", async (route) => {
    emptyCloseRequest = new URL(route.request().url());
    await route.fulfill({ json: { ok: true, message: "close empty terminal session source ok" } });
  });

  await expect(page.locator('[data-sf="all"]')).toContainText("all 5");
  await expect(page.locator('[data-sf="ssh"]')).toContainText("ssh 1");
  await expect(page.locator('[data-sf="tmux"]')).toContainText("tmux 2");
  await expect(page.locator('[data-sf="screen"]')).toBeHidden();
  await expect(page.locator("#session-rows")).toContainText("No active sessions");
  const emptySource = page.locator("#session-rows tr", { hasText: "tmux-empty" });
  const emptySourceCells = emptySource.locator("td");
  await expect(emptySourceCells.nth(3)).toHaveText("empty");
  await expect(emptySourceCells.nth(3).locator(".target-state")).toHaveClass(/state-empty/);
  await expect(emptySourceCells.nth(5)).toHaveText("—");
  await expect(emptySourceCells.nth(7)).toHaveText("—");
  await expect(page.locator("#session-rows tr", { hasText: "screen-root" }).getByRole("button", { name: "close" })).toHaveCount(0);

  await emptySource.getByRole("button", { name: "close" }).click();
  await expect(page.locator("#simple-confirm-message")).toContainText("stops only the empty tmux server");
  await page.locator("#simple-confirm-ok").click();
  await expect.poll(() => emptyCloseRequest && emptyCloseRequest.pathname).toBe("/api/services/web/terminal-sessions/tmux-empty/close-empty");
});

test("an unknown terminal session state is not shown as active", async ({ page }) => {
  const body = JSON.parse(JSON.stringify(dashboard));
  body.sessions.terminal.push({
    service: "web", check: "tmux-root", multiplexer: "tmux", user: "root",
    name: "pending", state: "unknown", identity: "$9:92", can_close: true,
  });
  await page.route("**/api/dashboard**", (route) => route.fulfill({ json: body }));
  await page.locator("#refresh-now").click();

  const state = page.locator("#session-rows tr", { hasText: "pending" }).locator("td").nth(3);
  await expect(state).toHaveText("unknown");
  await expect(state.locator(".target-state")).toHaveClass(/state-warning/);
});

test("a genuinely idle process reads 0% in both CPU columns", async ({ page }) => {
  // max_core carries omitempty, so a process at exactly 0% sends no field at all.
  // That must read the same as its CPU cell — "0%", a measured zero — not "—", which
  // means "unknown". The two columns are fed by the same sample: if one has a rate,
  // so does the other.
  await page.route("**/api/services/db", async (route) => {
    const body = JSON.parse(JSON.stringify(serviceDetail("db")));
    delete body.processes[0].cpu;
    delete body.processes[0].max_core;
    body.processes[0].has_cpu = true;
    body.processes[0].max_core_exact = true;
    await route.fulfill({ json: body });
  });
  await page.locator("#svc-row-db .row-toggle").click();

  const cells = page.locator('[data-service-detail="db"]')
    .getByRole("table", { name: "Service processes" })
    .locator("tbody tr").first().locator("td");
  await expect(cells.nth(4)).toContainText("0%");
  await expect(cells.nth(5)).toContainText("0%");
});

test("a stray process is named as one instead of reading as the principal", async ({ page }) => {
  // A stray carries the backend seed's role "main": the daemon labels every PID in
  // the unit's control group that way before a selector can name one. Showing that
  // role alone would say this is the service's principal process, which is the
  // opposite of the truth — nothing in the configuration accounts for it.
  await page.route("**/api/services/db", async (route) => {
    const body = JSON.parse(JSON.stringify(serviceDetail("db")));
    body.processes[0].stray = true;
    await route.fulfill({ json: body });
  });
  await page.locator("#svc-row-db .row-toggle").click();

  const cell = page.locator('[data-service-detail="db"]')
    .getByRole("table", { name: "Service processes" })
    .locator("tbody tr").first().locator("td").nth(3);
  await expect(cell).toHaveText("stray");
  await expect(cell.locator("[title]")).toHaveAttribute("title", /claimed by no process selector/);
});

test("an unmeasured busiest core is shown as a bound, not a reading", async ({ page }) => {
  // Below the daemon's thread-sampling floor there is no per-thread measurement, so
  // the process rate stands in as an upper bound and must be marked as one.
  await page.route("**/api/services/db", async (route) => {
    const body = JSON.parse(JSON.stringify(serviceDetail("db")));
    body.processes[0].max_core = body.processes[0].cpu;
    body.processes[0].max_core_exact = false;
    await route.fulfill({ json: body });
  });
  await page.locator("#svc-row-db .row-toggle").click();

  const cell = page.locator('[data-service-detail="db"]')
    .getByRole("table", { name: "Service processes" })
    .locator("tbody tr").first().locator("td").nth(5);
  await expect(cell).toContainText("96.25%");
  // Nothing marks the cell; the tooltip is what says the figure is an upper bound.
  await expect(cell.locator("[title]").first())
    .toHaveAttribute("title", /at most .* not measured per thread/);
});

test("service detail complements the row instead of repeating it", async ({ page }) => {
  await page.locator("#svc-row-web .row-toggle").click();
  const detail = page.locator('[data-service-detail="web"]');
  // The row already names the service and the grid is self-evident, so neither
  // heading survives; the name moved into the grid as the expansion's anchor.
  await expect(detail.getByRole("heading", { name: "General data" })).toHaveCount(0);
  // Pinned as "which heading opens the expansion" rather than "no heading is named
  // after the service": several headings embed the display name through a nested
  // control's aria-label (Preflight's run button among them), so a name-based
  // absence check either matches one of those or, keyed on the service key instead
  // of displayName's "Web server", passes vacuously.
  await expect(detail.locator("h2").first()).toHaveText(/^Graphs/);
  await expect(detail.locator(".runtime-grid > div", { hasText: "Name" }).first()).toContainText("Web server");
  // The process count and the whole-tree totals are grid fields; the lines that
  // restated them above the table are gone.
  await expect(detail.locator(".detail-summary")).toHaveCount(0);
  await expect(detail.locator(".detail-totals")).toHaveCount(0);
  await expect(detail.getByRole("heading", { name: "Processes" })).toBeVisible();
});

// A duplicated General data field and its table column are mutually exclusive at
// every width: exactly one of the pair is on screen, so the reading is never shown
// twice and never lost. The widths are driven here rather than left to the project
// viewports because neither project is wide enough to clear the 1420px breakpoint —
// devices["Desktop Chrome"] is 1280px, already inside compact mode.
test("detail fields appear only where their table column is hidden", async ({ page }) => {
  await page.locator("#svc-row-web .row-toggle").click();
  const detail = page.locator('[data-service-detail="web"]');
  const pairs = [
    { label: "IO R/W", field: ".col-dup-1420", column: "io" },
    { label: "Last event", field: ".col-dup-640", column: "last" },
    { label: "Strays", field: ".col-dup-640", column: "strays" },
  ];
  // One width per band of the responsive rules, with the columns each band still
  // shows spelled out. Asserting the expected column visibility rather than reading
  // it back is what makes the field assertion mean something: derived from the live
  // state, "column hidden so field visible" would hold with the CSS deleted.
  const bands = [
    { width: 1600, shows: ["io", "last", "strays"] }, // full table
    { width: 1200, shows: ["last", "strays"] }, // metric columns retired
    { width: 500, shows: [] }, // Last activity and Strays retired too
  ];
  for (const band of bands) {
    await page.setViewportSize({ width: band.width, height: 900 });
    for (const pair of pairs) {
      const at = `${pair.label} at ${band.width}px`;
      const field = detail.locator(`.runtime-grid > ${pair.field}`, { hasText: pair.label });
      const column = page.locator(`.services-table th[data-sort="${pair.column}"]`);
      if (band.shows.includes(pair.column)) {
        await expect(column, `${at}: column expected on screen`).toBeVisible();
        await expect(field, `${at}: column shown, field must not repeat it`).toBeHidden();
      } else {
        await expect(column, `${at}: column expected retired`).toBeHidden();
        await expect(field, `${at}: column hidden, field must carry the reading`).toBeVisible();
      }
    }
  }
});

test("service table re-renders preserve hydrated detail without more requests", async ({ page }) => {
  let detailRequests = 0;
  page.on("request", (request) => {
    if (new URL(request.url()).pathname.startsWith("/api/services/web")) detailRequests++;
  });

  await page.locator("#svc-row-web .row-toggle").click();
  const detail = page.locator('[data-service-detail="web"]');
  await expect(detail.locator('[data-service-metric-check="users"] svg')).toBeVisible();
  await page.waitForTimeout(100);
  const hydratedRequests = detailRequests;

  await page.locator("#svc-search").fill("web");
  await page.waitForTimeout(1100);

  await expect(detail.locator('[data-service-metric-check="users"] svg')).toBeVisible();
  expect(detailRequests).toBe(hydratedRequests);
});

test("notifier test asks for confirmation and posts one named notifier", async ({ page }) => {
  await page.locator("#notifiers-section > summary").click();
  const button = page.locator('[data-notifier-test="ops"]');
  await expect(button).toBeVisible();
  const request = page.waitForRequest((req) => req.method() === "POST" && new URL(req.url()).pathname === "/api/notifiers/ops/test");
  await button.click();
  await expect(page.locator("#simple-confirm")).toBeVisible();
  await page.locator('[data-simple-result="true"]').click();
  expect((await request).headers()["x-sermo-generation"]).toBe("7");
});

test("a service with unaccounted-for processes offers a confirmed reap", async ({ page }) => {
  // The count is only shown when it is not zero: a dash everywhere else would make
  // three leaked processes indistinguishable from a healthy service at a glance.
  await page.setViewportSize({ width: 1600, height: 900 });
  const cell = page.locator("#svc-row-stale td").nth(9);
  await expect(cell).toContainText("3");
  await expect(page.locator("#svc-row-web td").nth(9)).toContainText("—");

  const button = page.locator('[data-service-reap="stale"]');
  await expect(button).toHaveAttribute("aria-label", /Reap the 3 unaccounted-for process/);
  const request = page.waitForRequest((req) => req.method() === "POST"
    && new URL(req.url()).pathname === "/api/services/stale/reap");
  await button.click();
  // Signalling processes is always confirmed, like closing a session.
  await expect(page.locator("#simple-confirm")).toBeVisible();
  await page.locator('[data-simple-result="true"]').click();
  expect((await request).headers()["x-sermo-generation"]).toBe("7");
  expect((await request).headers()["x-sermo-csrf"]).toBe("1");
});

test("declining the reap confirmation signals nothing", async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 900 });
  let posted = 0;
  page.on("request", (req) => {
    if (req.method() === "POST" && new URL(req.url()).pathname.endsWith("/reap")) posted += 1;
  });
  await page.locator('[data-service-reap="stale"]').click();
  await expect(page.locator("#simple-confirm")).toBeVisible();
  await page.locator('[data-simple-result="false"]').click();
  await delay(100);
  expect(posted).toBe(0);
});

test("the reap button is disabled while an operation holds the service", async ({ page }) => {
  // A reap takes the same per-service operation lock as a restart, so offering it
  // mid-operation would only earn a 409. The disabled reason must also reach a
  // screen reader, through the same visually-hidden hint the other actions use.
  await page.setViewportSize({ width: 1600, height: 900 });
  await page.route("**/api/dashboard**", async (route) => {
    const body = JSON.parse(JSON.stringify(dashboard));
    body.services = body.services.map((s) => (s.name === "stale" ? { ...s, operation_active: true } : s));
    await route.fulfill({ json: body });
  });
  await page.reload();

  const button = page.locator('[data-service-reap="stale"]');
  await expect(button).toBeDisabled();
  const hintID = await button.getAttribute("aria-describedby");
  expect(hintID).toBeTruthy();
  await expect(page.locator(`#${hintID}`)).toHaveText("operation in progress");
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

test("application deep links expand after its inventory renders", async ({ page }) => {
  await page.goto("/#app:postgres");
  await expect(page.locator('[id="exp-app:postgres"]')).toContainText("16.3");
});

test("monitor toggles send one request even on a double click", async ({ page }) => {
  let servicePosts = 0;
  await page.route("**/api/services/web/unmonitor", async (route) => {
    servicePosts += 1;
    await delay(400);
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ok: true, message: "unmonitored" }) });
  });
  let watchPosts = 0;
  await page.route("**/api/watches/process-queue/unmonitor", async (route) => {
    watchPosts += 1;
    await delay(400);
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ok: true, message: "unmonitored" }) });
  });

  const serviceButton = page.locator('#svc-row-web [data-service-action="unmonitor"]');
  await serviceButton.click();
  await serviceButton.click({ force: true }).catch(() => {});
  const watchButton = page.locator('#wat-row-process-queue [data-watch-action="unmonitor"]');
  await watchButton.click();
  await watchButton.click({ force: true }).catch(() => {});

  await page.waitForTimeout(600);
  expect(servicePosts).toBe(1);
  expect(watchPosts).toBe(1);
});

test("monitor toggle stays guarded until the follow-up refresh lands", async ({ page }) => {
  let posts = 0;
  await page.route("**/api/services/web/unmonitor", async (route) => {
    posts += 1;
    await delay(200);
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ok: true, message: "unmonitored" }) });
  });
  await page.route("**/api/dashboard**", async (route) => {
    await delay(800);
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(dashboard) });
  });

  const button = page.locator('#svc-row-web [data-service-action="unmonitor"]');
  await button.click();
  // Response has landed but the follow-up dashboard reload is still in flight;
  // an unrelated re-render must not re-enable the button early.
  await page.waitForTimeout(400);
  await page.locator("#svc-search").fill("web");
  await button.click({ force: true }).catch(() => {});
  await page.waitForTimeout(1200);
  expect(posts).toBe(1);
});

test("a reload event paints the activity cell as info like the events table", async ({ page }) => {
  await expect(page.locator("#svc-row-db .activity-time")).toHaveClass(/activity-info/);
});

test("a failing services list alone does not dim the dashboard as disconnected", async ({ page }) => {
  const error = { status: 500, contentType: "application/json", body: JSON.stringify({ message: "boom" }) };
  await page.route("**/api/dashboard**", (route) => route.fulfill(error));
  await page.route("**/api/services", (route) => route.fulfill(error));

  await page.locator("#refresh-now").click();
  await expect(page.locator("#daemon-backend")).toHaveText("systemd");
  await expect(page.locator("body")).not.toHaveClass(/disconnected/);
  await expect(page.locator("#svc-row-web")).toBeVisible();
  await expect(page.locator("#err")).toContainText("keeping the last known list");

  // A cold load (no list ever rendered) must not claim it is keeping one.
  await page.goto("/");
  await expect(page.locator("#err")).toContainText("services unavailable");
  await expect(page.locator("#err")).not.toContainText("keeping the last known list");
});

test("daemon metrics use decimal byte rates and grouped counts", async ({ page }) => {
  // Sizes stay IEC binary (KiB, 1024); rates are SI decimal (KB/s, 1000).
  await expect(page.locator("#daemon-io-live")).toHaveText("2.05 KB/s");
  await expect(page.locator("#daemon-memory-live")).toContainText("1 MiB");
  await expect(page.locator("#daemon-fds")).toHaveText("12,345");
});

test("the dashboard dims as disconnected when every endpoint fails", async ({ page }) => {
  const error = { status: 500, contentType: "application/json", body: JSON.stringify({ message: "down" }) };
  await page.route("**/api/**", (route) => route.fulfill(error));
  await page.route("**/readyz**", (route) => route.fulfill(error));
  await page.route("**/livez**", (route) => route.fulfill(error));

  await page.locator("#refresh-now").click();
  await expect(page.locator("body")).toHaveClass(/disconnected/);
  await expect(page.locator("#err")).toContainText("Disconnected");
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

// The server no longer answers an API 401 with WWW-Authenticate, so a poll that
// loses its credential can no longer make the browser raise a modal password
// box on its own. The dashboard has to notice instead: it goes to /login, the
// one route that still challenges deliberately and then returns home.
test("a 401 from the API navigates to the login route", async ({ page }) => {
  await page.route("**/login", async (route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: "<title>login reached</title>" });
  });
  await page.route("**/api/**", async (route) => {
    await route.fulfill({
      status: 401,
      contentType: "application/json",
      body: JSON.stringify({ ok: false, message: "authentication required" }),
    });
  });

  await page.locator("#reload-btn").click();
  await page.waitForURL(/\/login$/, { timeout: 15000 });
  expect(new URL(page.url()).pathname).toBe("/login");
});
