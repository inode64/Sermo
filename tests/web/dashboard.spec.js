const { test, expect } = require("@playwright/test");
const AxeBuilder = require("@axe-core/playwright").default;

const delay = (milliseconds) => new Promise((resolve) => {
  setTimeout(resolve, milliseconds);
});

const services = [
  {
    name: "web", display_name: "Web server", category: "service", enabled: true,
    monitored: true, status: "active", state: "active", can_reload: true,
    uptime_seconds: 7200, status_observed_at: "2026-07-10T12:00:00Z", fds: 4096,
    buttons: [{ name: "flush-queue", label: "Flush queue" }],
  },
  {
    name: "db", display_name: "Database", category: "service", enabled: true,
    monitored: true, status: "active", state: "started", can_reload: true, fds: 512,
    uptime_seconds: 10800, status_observed_at: "2026-07-10T12:00:00Z",
    last_event: { time: "2026-07-10T11:59:00Z", kind: "reload", message: "config reloaded" },
  },
  {
    name: "stale", display_name: "Stale binary", category: "service", enabled: true,
    monitored: true, status: "active", state: "restart_required", state_reason: "stale_binary",
    uptime_seconds: 3600, status_observed_at: "2026-07-10T12:00:00Z", strays: 3,
  },
];

const dashboard = {
  generation: 7,
  services,
  sessions: {
    sources: [
      {
        kind: "ssh", service: "web", state: "partial",
        message: "1 terminal(s) could not be attributed safely",
        issues: [{ user: "root", terminal: "pts/0", pid: 95, start_ticks: 1200, can_close: true, managed_by_logind: true, message: "executable /usr/lib/sshd-session was replaced" }],
      },
      { kind: "ssh", service: "db", state: "available" },
      { kind: "tmux", service: "web", check: "tmux-root", user: "root", state: "available" },
      { kind: "tmux", service: "web", check: "tmux-empty", user: "root", state: "available", can_close_empty: true },
      { kind: "screen", service: "web", check: "screen-root", user: "root", state: "available" },
    ],
    ssh: [{ service: "web", user: "root", terminal: "pts/11", pid: 96, start_ticks: 1234, idle_seconds: 120, can_close: true, memory_ready: true, rss: 1048576, cpu_ready: true, cpu: 1.5, io_ready: true, io_read: 1000, io_write: 250 }],
    terminal: [
      { service: "web", check: "tmux-root", multiplexer: "tmux", user: "root", name: "ops", pids: [201], state: "attached", windows: 2, idle_seconds: 300, has_idle: true, memory_ready: true, rss: 2097152, cpu_ready: true, cpu: 2.5, io_ready: true, io_read: 2000, io_write: 500, identity: "$7:90", can_close: true },
      // screen reports no window count: the row shows the name alone.
      { service: "web", check: "screen-root", multiplexer: "screen", user: "root", name: "16128.pts-0.fixture", pids: [301], state: "attached", idle_seconds: 30, has_idle: true },
      { service: "web", check: "tmux-root", multiplexer: "tmux", user: "root", name: "build", pids: [202, 203], state: "detached", windows: 1, idle_seconds: 60, has_idle: true, memory_ready: true, rss: 524288, cpu_ready: true, cpu: 0, io_ready: true, io_read: 0, io_write: 0, identity: "$8:91", can_close: true },
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
  name: "raid-md9", display_name: "RAID md9", category: "storage",
  enabled: true, monitored: true, state: "ok", check_type: "raid",
  summary: "raid md9 healthy", interval: "1m", status_observed_at: "2026-07-10T12:00:00Z",
  metrics: [
    { name: "degraded", band: true, severity: "error", label: "Degraded arrays" },
    { name: "recovering", band: true, severity: "warning", label: "Recovering arrays" },
  ],
  readings: [
    { field: "degraded", label: "Degraded", value: "none", good: true },
    { field: "recovering", label: "Recovering", warning: "recovering" },
  ],
}, {
  name: "db-replication", display_name: "DB replication", category: "database",
  enabled: true, monitored: true, state: "ok", check_type: "replication",
  can_control_replication: true,
  summary: "replication ok: io and sql running, 0s behind (source 172.31.27.30)",
  interval: "1m", status_observed_at: "2026-07-10T12:00:00Z",
  metrics: [
    { name: "io_stopped", band: true, severity: "error", label: "IO thread" },
    { name: "sql_stopped", band: true, severity: "error", label: "SQL thread" },
    { name: "behind_seconds", unit: "s" },
  ],
  readings: [
    { field: "source_host", label: "Source", value: "172.31.27.30" },
    { field: "io_stopped", label: "IO thread", value: "running", good: true },
    { field: "sql_stopped", label: "SQL thread", value: "running", good: true },
    { field: "behind_seconds", label: "Behind", value: "0 s" },
  ],
}, {
  name: "geoip-database-freshness", display_name: "GeoIP database freshness", category: "files",
  enabled: true, monitored: true, state: "ok", check_type: "file", summary_configured: true,
  readings: [
    { field: "path", label: "Path", value: "/usr/share/GeoIP" },
    { field: "age", label: "Age", value: "8mo 1d" },
  ],
  summary: "GeoIP databases are current", interval: "12h", status_observed_at: "2026-07-10T12:00:00Z",
}, {
  name: "dead-letter", display_name: "Dead letter", category: "files",
  enabled: true, monitored: true, state: "ok", check_type: "file",
  summary: "size threshold clear", interval: "5m", status_observed_at: "2026-07-10T12:00:00Z",
  metrics: [{ name: "size", band: true, severity: "error", label: "Size threshold" }],
}, {
  name: "host-fds", display_name: "File descriptors", category: "system",
  enabled: true, monitored: true, state: "ok", check_type: "fds",
  summary: "fds 879072 allocated (no kernel limit)", interval: "1m",
  status_observed_at: "2026-07-10T12:00:00Z",
  readings: [{ field: "allocated", label: "Allocated", value: "879072" }],
}, {
  name: "net-wan", display_name: "WAN", category: "network",
  enabled: true, monitored: true, state: "ok", check_type: "net", keeps_sla: true,
  metrics: [{ name: "used_pct", unit: "%" }],
  readings: [
    { field: "interface", label: "Interface", value: "eth0" },
    { field: "driver", label: "Driver", value: "ice" },
    { field: "speed", label: "Speed", value: "25000 Mbps" },
    { field: "addresses", label: "Addresses", value: "192.0.2.10, 2001:db8::10" },
    { field: "state", label: "State", value: "up" },
    { field: "errors", label: "Errors total", value: "0 (total 0)" },
  ],
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
  name: "firewall-paused", display_name: "Firewall", category: "watch",
  enabled: true, monitored: false, state: "disabled", monitor: "previous", monitor_source: "web",
  monitor_changed_at: "2026-07-10T11:55:00Z", check_type: "firewall_rules", interval: "1m",
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
    { field: "bus", label: "Bus", value: "nvme" },
    { field: "health", label: "Health", value: "PASSED" },
    { field: "temperature", label: "temperature", value: "42 °C" },
  ],
  metrics: [{ name: "temperature", unit: "°C" }],
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
    checks: [{
      name: "latency", type: "http", ran: true, ok: true,
      message: "status 200 from https://internal-gateway.example.intranet:8443/healthz/deep?include=downstream,queue,storage&trace=verbose-diagnostic-identifier-0123456789",
    }, ...namedMetrics, ...(name === "web" ? [{
      // A failure the check itself graded an advisory: a SMART predicate holding
      // under a PASSED verdict reads warn, not fail, and blocks no action.
      name: "smart-sda", type: "smart", ran: true, ok: false, severity: "warning",
      message: "smart /dev/sda health=PASSED; reallocated 4 > 0",
    }] : [])],
    processes: [{
      pid: name === "web" ? 101 : 202, cmdline: [name], user: "root", role: "main", rss: 1048576,
      // 96.25% spread over threads, of which the busiest held 61.5% of one core:
      // max_core must be readable as its own figure, not confused with cpu.
      has_cpu: true, cpu: 96.25, threads: 4, max_core: 61.5, max_core_exact: true,
    }, ...(name === "web" ? [{
      // A resolved worker with a real path renders the truncated command label
      // the phone layout has to fit beside two usage bars.
      pid: 3089414, ppid: 101, exe: "/usr/sbin/webserverd", exe_resolved: true, user: "www", role: "worker",
      cmdline: ["/usr/sbin/webserverd", "--config", "/etc/webserverd/webserverd.conf", "--foreground"],
      rss: 2097152, has_cpu: true, cpu: 3.5, threads: 2, max_core: 3.5, max_core_exact: true,
    }, {
      // A grandchild: the tree indent is part of the command column's width.
      pid: 3089420, ppid: 3089414, exe: "/usr/sbin/webserverd", exe_resolved: true, user: "www", role: "worker",
      cmdline: ["/usr/sbin/webserverd", "--config", "/etc/webserverd/webserverd.conf", "--foreground", "--worker"],
      rss: 1048576, has_cpu: true, cpu: 1.5, threads: 1, max_core: 1.5, max_core_exact: true,
    }] : [])],
    process_totals: {
      count: name === "web" ? 3 : 1, rss: 1048576, io_read: 0, io_write: 0, fds: 5, threads: 1,
      has_cpu: true, cpu: 12.5, cpu_thread: 96.25, num_cpu: 4,
    },
    locks: [], sla: [],
    // The rule names and conditions a real host shows: seven columns of these
    // used to split their words letter by letter on a phone.
    rules: name === "web" ? [
      { name: "restart-if-worker-thread-hot", type: "remediation", action: "restart", condition: "active:restart-if-worker-thread-hot", window: "for 6m", progress: "0s/6m" },
      { name: "restart-on-stale-binary", type: "remediation", action: "restart", condition: "failed:stale-binary", window: "immediate", progress: "0/1" },
      { name: "alert-if-msglog-backlog-high", type: "alert", action: "alert", condition: "active:alert-if-msglog-backlog-high", window: "immediate", progress: "0/1", condition_true: true },
    ] : [],
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
        else if (path.endsWith("/sla") && url.searchParams.get("metric")) {
          const now = Date.now();
          // recovering shows one failing bucket so the amber clamp is testable;
          // every other band reads fully OK.
          const failing = url.searchParams.get("metric") === "recovering";
          body = {
            since: url.searchParams.get("since"),
            points: [
              { start: new Date(now - 30 * 60 * 1000).toISOString(), up: 60, total: 60, down_buckets: 0 },
              { start: new Date(now - 5 * 60 * 1000).toISOString(), up: failing ? 20 : 60, total: 60, down_buckets: failing ? 3 : 0 },
            ],
          };
        }
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
        else if (path.startsWith("/api/watches/") && path.endsWith("/metrics")) {
          const now = Date.now();
          body = {
            check: "net", metric: url.searchParams.get("metric"),
            since: url.searchParams.get("since"), unit: "%",
            summary: { count: 2, avg: 12, min: 8, max: 16 },
            points: [
              { start: new Date(now - 20 * 60 * 1000).toISOString(), n: 1, avg: 8, min: 8, max: 8 },
              { start: new Date(now - 5 * 60 * 1000).toISOString(), n: 1, avg: 16, min: 16, max: 16 },
            ],
          };
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
  // Storage (raid-md9) · Network · System · service-scoped: grouping follows
  // the check-type family, not the category label.
  await expect(page.locator("#watch-rows .group-row")).toHaveCount(4);
  await expect(page.locator("#wat-row-process-queue .watch-scope")).toHaveText("service");
  // Host scope is the panel default and is not repeated after every name.
  await expect(page.locator("#wat-row-storage-data .watch-scope")).toHaveCount(0);
  await page.locator("#mount-group-toggle").click();
  await expect(page.locator("#mount-rows .group-row")).toHaveCount(2);
  await page.locator('#watch-rows [data-group-name="Network"]').click();
  await expect(page.locator("#wat-row-icmp-gateway")).toBeHidden();
  await expect(page.locator("#wat-row-firewall-paused")).toBeHidden();
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

test("stale binary has a distinct restart-required state and visible reason", async ({ page }) => {
  const row = page.locator("#svc-row-stale");
  const service = row.locator("td").nth(0);
  const state = row.locator("td").nth(2);
  await expect(state).toContainText("restart required");
  await expect(state.locator(".state-reason")).toHaveCount(0);
  await expect(service.locator(".svc-state-note")).toHaveText("binary replaced on disk");
  await expect(row).toHaveClass(/row-warning/);

  await row.locator(".row-toggle").click();
  const detail = page.locator('[data-service-detail="stale"]');
  await expect(detail.locator(".runtime-grid .state-reason")).toHaveText("binary replaced on disk");
});

// splitWordsIn lists the words (4+ characters) inside selector that a narrow
// column broke across two line boxes. Truncated cells, headings, buttons and
// screen-reader tables are excluded: they are clipped or hidden on purpose.
async function splitWordsIn(page, selector) {
  return page.locator(selector).evaluate((root) => {
    const out = [];
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      const el = node.parentElement;
      if (!el || el.getClientRects().length === 0 || el.closest(".truncate, .visually-hidden, thead, button")) continue;
      const words = /[A-Za-z0-9./-]{4,}/g;
      let match;
      while ((match = words.exec(node.textContent))) {
        const range = document.createRange();
        range.setStart(node, match.index);
        range.setEnd(node, match.index + match[0].length);
        if (range.getClientRects().length > 1) out.push(match[0]);
      }
    }
    return out;
  });
}

test("phone session rows keep their words whole", async ({ page }) => {
  await page.setViewportSize({ width: 412, height: 915 });
  await page.locator("#sessions-section").scrollIntoViewIfNeeded();
  // A path in a note may wrap after one of its slashes, which is a normal line
  // break, not a split word.
  const split = await splitWordsIn(page, "#sessions-section .sessions-table");
  expect(split.filter((token) => !token.includes("/"))).toEqual([]);
  const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(scrollWidth).toBeLessThanOrEqual(page.viewportSize().width);
});

// A PID is not something a phone acts on, and the close button names the
// session it closes, so the column goes and the table still fits the device.
test("phone session rows hide the PID column", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "mobile", "phone layout only");
  const sessions = page.getByRole("table", { name: "Current SSH, tmux and screen sessions" });
  await expect(sessions.locator("thead th", { hasText: "PID" })).toBeHidden();
  await expect(sessions.locator("thead th", { hasText: "Session" })).toBeVisible();
  const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(scrollWidth).toBeLessThanOrEqual(page.viewportSize().width);
});

// Every session close button is one icon on every device; what it closes is
// its accessible name and its tooltip, so the column stays narrow without
// losing the words a screen reader or a hover needs.
// A tmux session lists its window count under its name; screen reports none,
// and the cell used to fill the gap with a dash that read as a value.
test("a screen session shows its name without a placeholder dash", async ({ page }) => {
  const sessions = page.getByRole("table", { name: "Current SSH, tmux and screen sessions" });
  const screenRow = sessions.locator("tr", { hasText: "16128.pts-0.fixture" });
  await expect(screenRow.locator("td").nth(2)).toHaveText("16128.pts-0.fixture");
  await expect(screenRow.locator("td").nth(2).locator(".muted")).toHaveCount(0);
  const tmuxRow = sessions.locator("tr", { hasText: "ops" }).first();
  await expect(tmuxRow.locator("td").nth(2)).toContainText("2 windows");
});

test("session close buttons are labelled icons", async ({ page }) => {
  const sessions = page.getByRole("table", { name: "Current SSH, tmux and screen sessions" });
  const closers = sessions.locator("[data-ssh-session-close], [data-terminal-session-close], [data-empty-session-close]");
  await expect(closers.first()).toBeVisible();
  const buttons = await closers.evaluateAll((els) => els.map((el) => ({
    icon: el.classList.contains("icon-btn"), text: el.textContent.trim(), label: el.getAttribute("aria-label"), title: el.getAttribute("title"),
  })));
  expect(buttons.length).toBeGreaterThan(1);
  for (const b of buttons) {
    expect(b.icon).toBe(true);
    expect(b.text).toBe("✕");
    expect(b.label).toMatch(/^Close /);
    expect(b.title).toBe(b.label);
  }
  await expect(sessions.getByRole("button", { name: "Close SSH session pts/11 of root" })).toBeVisible();
});

test("phone mount rows keep their words whole", async ({ page }) => {
  await page.setViewportSize({ width: 412, height: 915 });
  await page.locator("#mounts-section").scrollIntoViewIfNeeded();
  const headings = await page.locator("#mounts-section .mount-table thead th").evaluateAll((cells) =>
    cells.filter((th) => th.getClientRects().length > 0).map((th) => th.textContent.trim()).filter(Boolean));
  expect(headings).toEqual(["Name", "Path", "State", "Actions"]);
  expect(await splitWordsIn(page, "#mounts-section .mount-table")).toEqual([]);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBe(0);
});

test("phone watch rows keep device, path and address tokens whole", async ({ page }) => {
  await page.setViewportSize({ width: 412, height: 915 });
  await page.locator("#watches-section").scrollIntoViewIfNeeded();
  // Devices, addresses and short values stay whole; only a token longer than
  // the room a phone can spare (a long path) may still break inside.
  const split = await splitWordsIn(page, "#watches-section");
  expect(split.filter((token) => token.length <= 12)).toEqual([]);
  // Against the device width, not clientWidth: under mobile emulation the
  // layout viewport grows with an overflowing document and hides it.
  const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(scrollWidth).toBeLessThanOrEqual(page.viewportSize().width);
});

// Typed watch tables sit in groups inside one cell of the watches table, so
// their minimum width used to become the page's: a storage usage bar or a long
// state pill widened the document a few seconds after load, once the watches
// rendered, and every panel shifted sideways on a phone.
test("phone watch groups never widen the page", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "mobile", "phone layout only");
  await page.locator("#watches-section").scrollIntoViewIfNeeded();
  const box = await page.evaluate(() => {
    const section = document.querySelector("#watches-section").getBoundingClientRect();
    const groups = [...document.querySelectorAll("#watches-section .watch-type-group")];
    return {
      scrollWidth: document.documentElement.scrollWidth,
      tablesPastSection: [...document.querySelectorAll("#watches-section table")].filter((t) => t.getBoundingClientRect().right > section.right + 1).length,
      groupsScrolling: groups.filter((g) => g.scrollWidth > g.clientWidth + 1).length,
      groups: groups.length,
    };
  });
  expect(box.groups).toBeGreaterThan(0);
  expect(box.scrollWidth).toBeLessThanOrEqual(page.viewportSize().width);
  expect(box.tablesPastSection).toBe(0);
  // The phone rules make every fixture table fit outright; the group's own
  // scroll stays a last resort.
  expect(box.groupsScrolling).toBe(0);
});

test("expanded check rows keep their identifiers whole on a desktop", async ({ page }) => {
  await page.locator("#svc-row-web .row-toggle").click();
  await expect(page.locator("#services-section .service-detail")).toBeVisible();
  expect(await splitWordsIn(page, "#services-section .detail-checks-table")).toEqual([]);
});

test("application rows keep their last activity inside a cell", async ({ page }) => {
  // The activity value used to land directly in the <tr>, so the browser
  // rendered it in an anonymous cell outside the row's highlight and beyond
  // the expansion's colspan.
  const tags = await page.locator("#app-row-nginx > *").evaluateAll((cells) => cells.map((cell) => cell.tagName));
  expect(tags).toEqual(["TD", "TD", "TD", "TD", "TD"]);
  await page.locator("#apps-section .row-toggle").first().click();
  const widths = await page.evaluate(() => {
    const row = document.querySelector("#app-row-nginx").getBoundingClientRect();
    const expansion = document.querySelector("#apps-section .exp-row > td").getBoundingClientRect();
    return { row: Math.round(row.width), expansion: Math.round(expansion.width) };
  });
  expect(widths.expansion).toBe(widths.row);
});

test("row expansions use one heading size", async ({ page }) => {
  await page.locator("#apps-section .row-toggle").first().click();
  const sizes = await page.locator("#apps-section .exp-row").evaluate((row) => {
    const size = (el) => el && parseFloat(getComputedStyle(el).fontSize);
    const headings = [...row.querySelectorAll("h2, h3")];
    return { availability: size(headings.find((h) => h.textContent.startsWith("Availability"))), events: size(headings.find((h) => h.textContent.startsWith("Recent events"))) };
  });
  expect(sizes.availability).toBe(sizes.events);
});

test("expanded detail tables keep their headers in flow", async ({ page }) => {
  await page.locator("#svc-row-web .row-toggle").click();
  const detail = page.locator("#services-section .service-detail");
  await expect(detail).toBeVisible();
  // The section table's own header is sticky; a nested table's header scrolls
  // away with its row and never pins itself under the top bar.
  await expect(page.locator("#services-section .services-table > thead > tr > th").first()).toHaveCSS("position", "sticky");
  const nested = await detail.locator("table thead th").evaluateAll((cells) =>
    cells.map((th) => getComputedStyle(th).position));
  expect(nested.length).toBeGreaterThan(0);
  expect(nested.every((position) => position === "static")).toBe(true);

  await page.setViewportSize({ width: 412, height: 915 });
  // Fixed phone columns hold every nested heading inside its own cell instead
  // of clipping it against the next one ("NAMI STAT TTL ..."). Screen-reader
  // tables are visually collapsed on purpose and stay out of the measurement.
  const clipped = await detail.locator("table:not(.visually-hidden) thead th").evaluateAll((cells) => cells.filter((th) => {
    const range = document.createRange();
    range.selectNodeContents(th);
    const text = range.getBoundingClientRect();
    const cell = th.getBoundingClientRect();
    return text.right > cell.right + 1;
  }).map((th) => th.textContent));
  expect(clipped).toEqual([]);
});

// A phone reads a service's state and acts on it: the per-check and per-rule
// tables are desk work, so the expansion hides them there and keeps the lock
// table readable.
test("expanded checks and rules hide on a phone and the lock table stays readable", async ({ page }) => {
  await page.setViewportSize({ width: 412, height: 915 });
  await page.locator("#svc-row-web .row-toggle").click();
  const detail = page.locator("#services-section .service-detail");
  await expect(detail).toBeVisible();
  await expect(detail.locator('[data-detail-section="checks"]')).toBeHidden();
  await expect(detail.locator('[data-detail-section="rules"]')).toBeHidden();
  await expect(detail.locator("h2", { hasText: "Named locks" })).toBeVisible();
  const visibleHeadings = (table) => detail.locator(`${table} thead th`).evaluateAll((cells) =>
    cells.filter((th) => th.getClientRects().length > 0).map((th) => th.textContent.trim()));
  expect(await visibleHeadings(".detail-locks-table")).toEqual(["Name", "State", "Owner", "Reason", "Actions"]);
  const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(scrollWidth).toBeLessThanOrEqual(page.viewportSize().width);

  // Back on a desktop width both sections return with their tables.
  await page.setViewportSize({ width: 1280, height: 900 });
  await expect(detail.locator('[data-detail-section="checks"] .detail-checks-table')).toBeVisible();
  await expect(detail.getByRole("table", { name: "Remediation rules" })).toBeVisible();
});

test("invalid application configuration stays a visible warning", async ({ page }) => {
  const body = JSON.parse(JSON.stringify(dashboard));
  const service = body.services.find((item) => item.name === "db");
  service.state = "warning";
  service.state_reason = "configuration_invalid";
  service.check_health = "warning";
  await page.route("**/api/dashboard**", (route) => route.fulfill({ json: body }));
  await page.locator("#refresh-now").click();

  const row = page.locator("#svc-row-db");
  await expect(row).toHaveClass(/row-warning/);
  await expect(row.locator("td").nth(2)).toContainText("warning");
  await expect(row.locator("td").nth(2).locator(".state-reason")).toHaveCount(0);
  await expect(row.locator("td").nth(0).locator(".svc-state-note")).toHaveText("configuration invalid");
});

test("service warning reason sits below the service instead of widening State", async ({ page }) => {
  await page.route("**/api/dashboard**", async (route) => {
    const body = JSON.parse(JSON.stringify(dashboard));
    body.services.push({
      // A long identity squeezes the State column on a phone, which is where a
      // pill or a reason used to split inside a word.
      name: "degraded", display_name: "Degraded workload with a fairly long display name", category: "service", enabled: true,
      monitored: true, status: "failed", state: "warning", state_reason: "failed_unit_live_process",
      uptime_seconds: 1800, status_observed_at: "2026-07-10T12:00:00Z",
    });
    await route.fulfill({ json: body });
  });
  await page.reload();

  const row = page.locator("#svc-row-degraded");
  const serviceCell = row.locator("td").nth(0);
  const stateCell = row.locator("td").nth(2);
  const note = serviceCell.locator(".svc-state-note");
  await expect(page.locator("#svc-row-web .svc-state-note")).toHaveCount(0);
  await expect(stateCell.locator(".target-state")).toHaveText("warning");
  await expect(stateCell.locator(".state-reason")).toHaveCount(0);
  await expect(note).toHaveText("init unit failed; workload healthy");

  const positions = await serviceCell.evaluate((cell) => {
    const main = cell.querySelector(".svc-main").getBoundingClientRect();
    const message = cell.querySelector(".svc-state-note").getBoundingClientRect();
    return { mainBottom: main.bottom, messageTop: message.top };
  });
  expect(positions.messageTop).toBeGreaterThanOrEqual(positions.mainBottom);

  await page.setViewportSize({ width: 412, height: 915 });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBe(0);
  // Narrow cells wrap the reason and the state pill between words: no word is
  // split across two line boxes.
  const wordsIntact = await page.evaluate(() => {
    const intact = (el) => {
      const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
      for (let node = walker.nextNode(); node; node = walker.nextNode()) {
        const words = /\S+/g;
        let match;
        while ((match = words.exec(node.textContent))) {
          const range = document.createRange();
          range.setStart(node, match.index);
          range.setEnd(node, match.index + match[0].length);
          if (range.getClientRects().length > 1) return false;
        }
      }
      return true;
    };
    return {
      note: intact(document.querySelector("#svc-row-degraded .svc-state-note")),
      badge: intact(document.querySelector("#svc-row-stale .target-state")),
    };
  });
  expect(wordsIntact).toEqual({ note: true, badge: true });
});

test("paused monitoring is distinct from disabled configuration", async ({ page }) => {
  const paused = page.locator("#wat-row-firewall-paused");
  await expect(paused.locator(".target-state")).toHaveText("monitoring paused");
  await expect(paused).toContainText("via web UI");
  // Badge, source and time each take their own line.
  expect(await paused.innerHTML()).toMatch(/<br><span class="muted">(<!--[^>]*-->)?via web UI<br>/);

  await paused.locator(".row-toggle").click();
  const detail = page.locator('[id="exp-wat:firewall-paused"]');
  await expect(detail).toContainText("Monitoring");
  expect(await detail.innerHTML()).toMatch(/<br><span class="muted">(<!--[^>]*-->)?via web UI<br>/);
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

test("a SMART device graphs only the indicators it publishes", async ({ page }) => {
  const series = page.waitForRequest((request) => {
    const url = new URL(request.url());
    return url.pathname === "/api/watches/smart-sdb/metrics" && url.searchParams.get("metric") === "temperature";
  });
  await page.locator("#wat-row-smart-sdb .row-toggle").click();
  await series;

  const detail = page.locator('[id="exp-wat:smart-sdb"]');
  await expect(detail.locator('[data-watch-metric="temperature"]')).toBeVisible();
  await expect(detail.locator("[data-watch-metric]")).toHaveCount(1);
  await expect(detail).not.toContainText("No data yet for this window.");
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

test("failed watch filter includes missing devices", async ({ page }) => {
  await page.locator('#watch-filters [data-wf="failed"]').click();
  await expect(page.locator('#watch-filters [data-wf="failed"]')).toHaveClass(/f-active/);
  await expect(page.locator("#wat-row-smart-sdz")).toBeVisible();
  await expect(page.locator("#wat-row-storage-data")).toBeHidden();
});

test("active service filter isolates the active state", async ({ page }) => {
  await page.locator('#svc-filters [data-f="active"]').click();
  await expect(page.locator('#svc-filters [data-f="active"]')).toHaveClass(/f-active/);
  await expect(page.locator("#svc-row-web")).toBeVisible();
  await expect(page.locator("#svc-row-db")).toBeHidden();
  await expect(page.locator("#svc-row-stale")).toBeHidden();
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

test("stop confirms without offering engine preflight", async ({ page }) => {
  await page.locator('#svc-row-web [data-service-action="stop"]').click();
  await expect(page.locator("#action-confirm")).toBeVisible();
  await expect(page.locator("#confirm-body")).toContainText("will not start the service again");
  await expect(page.locator("#confirm-preflight-btn")).toBeDisabled();
  await expect(page.locator("#confirm-preflight-hint")).toHaveText("preflight not available for this action");
  await page.keyboard.press("Escape");
});

async function reloadBehindConfirmation(page) {
  await page.route("**/api/dashboard**", (route) => route.fulfill({ json: {
    ...dashboard, generation: 8, daemon: { ...dashboard.daemon, hostname: "reloaded" },
  } }));
  // The real timer can refresh while a modal is open; trigger that same load
  // through the existing refresh command without interacting through the modal.
  await page.evaluate(() => document.getElementById("refresh-now").click());
  await expect(page).toHaveTitle(/Sermo - reloaded/);
}

const generationConfirmationCases = [
  { name: "service", button: '#svc-row-web [data-service-action="restart"]', dialog: "#action-confirm", confirm: "#confirm-action-btn", path: "/api/services/web/restart" },
  { name: "operator button", button: '[data-service="web"][data-service-button="flush-queue"]', dialog: "#simple-confirm", confirm: "#simple-confirm-ok", path: "/api/services/web/button/flush-queue" },
  { name: "watch", button: '#wat-row-db-replication [data-watch-action="replication-start"]', dialog: "#simple-confirm", confirm: "#simple-confirm-ok", path: "/api/watches/db-replication/replication-start" },
  { name: "notifier", section: "#notifiers-section", button: '[data-notifier-test="ops"]', dialog: "#simple-confirm", confirm: "#simple-confirm-ok", path: "/api/notifiers/ops/test" },
  { name: "SSH session", section: "#sessions-section", button: '[data-ssh-session-pid="96"]', dialog: "#simple-confirm", confirm: "#simple-confirm-ok", path: "/api/services/web/sessions/96/close" },
  { name: "mount", section: "#mounts-section", button: '[data-mount="data.mount"][data-mount-action="umount"]', dialog: "#mount-umount-confirm", confirm: '[data-mount-umount-result="true"]', path: "/api/mounts/data.mount/umount" },
];

for (const scenario of generationConfirmationCases) {
  test(`${scenario.name} confirmation keeps its generation across reload`, async ({ page }) => {
    if (scenario.section && await page.locator(scenario.section).getAttribute("open") === null) {
      await page.locator(`${scenario.section} > summary`).click();
    }
    await page.locator(scenario.button).click();
    await expect(page.locator(scenario.dialog)).toBeVisible();
    await reloadBehindConfirmation(page);
    await page.route(`**${scenario.path}**`, (route) => route.fulfill({
      status: 412, json: { ok: false, message: "configuration changed; refresh and try again" },
    }));
    const request = page.waitForRequest((req) => req.method() === "POST" && new URL(req.url()).pathname === scenario.path);
    await page.locator(scenario.confirm).click();
    expect((await request).headers()["x-sermo-generation"]).toBe("7");
    await expect(page.locator("#err")).toContainText("configuration changed");
  });
}

test("confirmation preflight keeps the reviewed generation", async ({ page }) => {
  await page.locator('#svc-row-web [data-service-action="restart"]').click();
  await expect(page.locator("#action-confirm")).toBeVisible();
  await reloadBehindConfirmation(page);
  const request = page.waitForRequest((req) => req.method() === "POST" && new URL(req.url()).pathname === "/api/services/web/preflight");
  await page.locator("#confirm-preflight-btn").click();
  expect((await request).headers()["x-sermo-generation"]).toBe("7");
  await page.keyboard.press("Escape");
});

test("a failed confirmation context cannot authorize an action", async ({ page }) => {
  await page.route("**/api/services/web", (route) => route.fulfill({
    headers: { "X-Sermo-Generation": "8" }, json: serviceDetail("web"),
  }));
  await page.locator('#svc-row-web [data-service-action="restart"]').click();
  await expect(page.locator("#action-confirm")).toBeVisible();
  await expect(page.locator("#confirm-body")).toContainText("configuration changed");
  await expect(page.locator("#confirm-action-btn")).toBeDisabled();
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
    return Math.min(24 * rem, strip.ownerDocument.defaultView.innerWidth * 0.30);
  });
  expect(parseFloat(checkBandHeight)).toBeCloseTo(parseFloat(serviceBandHeight), 1);
  expect(parseFloat(checkCellMinWidth)).toBeCloseTo(expectedCheckCellMinWidth, 1);
  // The mock's single check sample sits 10 minutes into a 24h window, so every
  // bar before it must stay hatched rather than inherit the window's ratio.
  expect(await cell.locator(".sla-bar-seg.sla-gap").count()).toBeGreaterThan(0);
  await expect(cell.locator(".sla-count")).toHaveText("40/40");
});

// A host-wide count says how many; only the per-service breakdown says who. The
// figures come from the service list the dashboard already holds, so the section
// costs no request, and each name opens that service rather than just printing it.
// A watch sub-table names the column after the number it shows: fds reads
// "Allocated" with the live count, never a generic "Value" header pointing at
// whatever reading happened to come first.
test("the fds watch table names its column Allocated", async ({ page }) => {
  const row = page.locator("#wat-row-host-fds");
  await expect(row).toContainText("879072");
  const fdsHeader = page.locator('th[data-watch-type-sort-type="fds"][data-watch-type-sort="allocated"]');
  await expect(fdsHeader.first()).toBeVisible();
  await expect(fdsHeader.first()).toHaveText("Allocated");
  await expect(page.locator('th[data-watch-type-sort-type="fds"][data-watch-type-sort="value"]')).toHaveCount(0);
});

test("diagnostic watch columns keep freshness, network identity, and SMART interface visible", async ({ page }) => {
  const geoip = page.locator("#wat-row-geoip-database-freshness");
  const network = page.locator("#wat-row-net-wan");
  const smart = page.locator("#wat-row-smart-sdb");

  await expect(page.locator('th[data-watch-type-sort-type="file-summary"][data-watch-type-sort="age"]')).toHaveText("Age");
  await expect(geoip).toContainText("8mo 1d");
  await expect(page.locator('th[data-watch-type-sort-type="net"][data-watch-type-sort="driver"]')).toHaveText("Driver");
  await expect(page.locator('th[data-watch-type-sort-type="net"][data-watch-type-sort="speed"]')).toHaveText("Speed");
  await expect(page.locator('th[data-watch-type-sort-type="net"][data-watch-type-sort="addresses"]')).toHaveText("IP");
  await expect(network).toContainText("ice");
  await expect(network).toContainText("25000 Mbps");
  await expect(network).toContainText("192.0.2.10, 2001:db8::10");
  await expect(page.locator('th[data-watch-type-sort-type="smart"][data-watch-type-sort="bus"]')).toHaveText("Interface");
  await expect(smart).toContainText("nvme");

  if ((page.viewportSize() || {}).width > 1024) {
    await expect(geoip.locator('[data-watch-type-column="age"]')).toBeVisible();
    await expect(network.locator('[data-watch-type-column="driver"]')).toBeVisible();
    await expect(network.locator('[data-watch-type-column="speed"]')).toBeVisible();
    await expect(network.locator('[data-watch-type-column="addresses"]')).toBeVisible();
    await expect(smart.locator('[data-watch-type-column="bus"]')).toBeVisible();
  }
});

test("an fds watch names the services holding the descriptors", async ({ page }) => {
  await page.locator("#wat-row-host-fds .row-toggle").click();
  const held = page.locator('[id="exp-wat:host-fds"] .count-holder');
  await expect(held.first()).toContainText("web");
  // web holds 4096 of the 4608 attributed to services.
  await expect(held.first()).toContainText("88.89%");
  await expect(page.locator('[id="exp-wat:host-fds"]')).toContainText("Held by");

  await held.first().locator("button").click();
  await expect(page.locator('[data-service-detail="web"]')).toBeVisible();
});

// The page never scrolls sideways: not on a phone, not on a tablet, not on a
// laptop, and not while a row carries a warning reason, a long unbroken
// diagnostic or an expanded detail. A wide cell wraps instead of widening the
// table — the alternative is the whole dashboard deforming exactly when a
// service fails, which is when it is being read.
test("no viewport lets the page scroll sideways", async ({ page }) => {
  for (const [width, height] of [[1440, 900], [1366, 768], [834, 1112], [412, 915]]) {
    await page.setViewportSize({ width, height });
    await page.locator("#svc-row-web .row-toggle").click();
    await expect(page.locator('[data-service-detail="web"]')).toBeVisible();
    await page.locator("#wat-row-net-wan .row-toggle").click();
    await expect(page.locator('[id="exp-wat:net-wan"]')).toBeVisible();
    const overflow = await page.evaluate(() => {
      const doc = document.documentElement;
      return doc.scrollWidth - doc.clientWidth;
    });
    expect(overflow, `${width}x${height} overflows by ${overflow}px`).toBe(0);
    // close them again so the next width re-measures a fresh expansion
    await page.locator("#svc-row-web .row-toggle").click();
    await page.locator("#wat-row-net-wan .row-toggle").click();
  }
});

// The events feed opens on the last 24 hours, and an explicitly healthy state
// reading renders green — "none degraded" answers at a glance where a 0 only
// encodes it.
test("events default to the last day and state readings colour by health", async ({ page }) => {
  await expect(page.locator("#event-range")).toHaveValue("24h");
  await page.locator("#wat-row-raid-md9 .row-toggle").click();
  const exp = page.locator('[id="exp-wat:raid-md9"]');
  await expect(exp.locator(".watch-reading-value.good")).toHaveText("none");
  await expect(exp.locator(".watch-reading-value.inactive")).toHaveText("recovering");
});

// An available source with nobody connected has nothing to say and nothing to
// close, so it renders no row at all. Its ssh sessions live under a different
// source key, so the web ssh session surviving proves the filter matched the
// empty db source, not the kind. The tmux empty-server row (a real process an
// admin can kill) is covered separately.
test("an available ssh source with no sessions renders no row", async ({ page }) => {
  await expect(page.locator("#session-rows tr", { hasText: "pts/11" })).toHaveCount(1);
  const empty = page.locator("#session-rows tr", { hasText: "No active sessions" });
  await expect(empty).toHaveCount(1);
  await expect(empty).toContainText("tmux");
});

// A configured operator button renders with its label, confirms, and posts to
// its own route; the command never reaches the browser.
test("a service operator button confirms and posts its route", async ({ page }) => {
  let buttonURL = null;
  await page.route("**/api/services/web/button/flush-queue", async (route) => {
    buttonURL = new URL(route.request().url());
    await route.fulfill({ json: { ok: true, message: "Flush queue: ok" } });
  });
  const row = page.locator("#svc-row-web");
  await row.getByRole("button", { name: /Flush queue/ }).click();
  await expect(page.locator("#simple-confirm-message")).toContainText("Flush queue");
  await page.locator("#simple-confirm-ok").click();
  await expect.poll(() => buttonURL && buttonURL.pathname).toBe("/api/services/web/button/flush-queue");
});

// The manual replication start is the DBA's repair executed by Sermo: the
// button explains what runs, confirms, and posts the replication-start action.
test("a replication watch offers its manual start behind confirmation", async ({ page }) => {
  let startURL = null;
  await page.route("**/api/watches/db-replication/replication-start", async (route) => {
    startURL = new URL(route.request().url());
    await route.fulfill({ json: { ok: true, message: "replication started" } });
  });
  const row = page.locator("#wat-row-db-replication");
  await expect(row).toContainText("172.31.27.30");
  await row.getByRole("button", { name: /Start replication/ }).click();
  await expect(page.locator("#simple-confirm-message")).toContainText("START REPLICA");
  await page.locator("#simple-confirm-ok").click();
  await expect.poll(() => startURL && startURL.pathname).toBe("/api/watches/db-replication/replication-start");
});

// A boolean state renders as an SLA-style band, never a line chart: a line
// through 0/1 draws slopes that never happened. The band reuses the service SLA
// panel wholesale, a warning-severity band caps its failing colour at amber, and
// a file watch — which keeps no availability at all — still gets its size band.
test("state metrics render as bands, amber for warnings", async ({ page }) => {
  const degradedSeries = page.waitForRequest((request) => {
    const url = new URL(request.url());
    return url.pathname === "/api/watches/raid-md9/sla" && url.searchParams.get("metric") === "degraded";
  });
  await page.locator("#wat-row-raid-md9 .row-toggle").click();
  await degradedSeries;
  const exp = page.locator('[id="exp-wat:raid-md9"]');
  const degraded = exp.locator('[data-band-metric="degraded"]');
  await expect(degraded).toContainText("Degraded arrays");
  await expect(degraded.locator(".sla-bars .sla-bar-seg").first()).toBeVisible();
  // no line chart for a band metric, and no metric row addressed by the line id
  await expect(exp.locator('[data-watch-metric="degraded"]')).toHaveCount(0);
  await expect(degraded.locator("svg")).toHaveCount(0);

  // recovering: the failing bucket wears amber, never the red-scale classes
  const recovering = exp.locator('[data-band-metric="recovering"]');
  await expect(recovering.locator(".sla-bar-seg.sla-down-low").first()).toBeVisible();
  await expect(recovering.locator(".sla-bar-seg.sla-down-mid, .sla-bar-seg.sla-down-high, .sla-bar-seg.sla-down-full")).toHaveCount(0);

  // the file watch has no Availability section, yet its size band renders
  await page.locator("#wat-row-dead-letter .row-toggle").click();
  const dead = page.locator('[id="exp-wat:dead-letter"]');
  await expect(dead.locator('[data-band-metric="size"] .sla-bars')).toBeVisible();
  await expect(dead.getByRole("heading", { name: "Availability" })).toHaveCount(0);
});

// A host watch that publishes a numeric reading graphs it with the panel a
// service check's metric gets — same markup, same window selector, same chart.
// Only the request differs: a watch has one check, so ?metric= alone names the
// series. Its Graphs window is separate from its Availability one, because the
// two read different series.
test("a watch graphs its numeric reading with the service metric panel", async ({ page }) => {
  const series = page.waitForRequest((request) => {
    const url = new URL(request.url());
    return url.pathname === "/api/watches/net-wan/metrics" && url.searchParams.get("metric") === "used_pct";
  });
  await page.locator("#wat-row-net-wan .row-toggle").click();
  await series;

  const panel = page.locator('[id="exp-wat:net-wan"] [data-watch-metric="used_pct"]');
  await expect(panel).toContainText("used pct");
  await expect(panel).toContainText("avg 12%");
  await expect(panel.locator("svg")).toBeVisible();

  const weekly = page.waitForRequest((request) => {
    const url = new URL(request.url());
    return url.pathname === "/api/watches/net-wan/metrics" && url.searchParams.get("since") === "168h";
  });
  await page.locator('[id="exp-wat:net-wan"] [data-window-kind="setWatchMetricWin"][data-window-value="168h"]').click();
  await weekly;
  // Moving the graphs window must not drag the availability panel with it.
  await expect(page.locator('[id="exp-wat:net-wan"] [data-window-kind="setSLAWin"][data-window-value="168h"]'))
    .toHaveAttribute("aria-pressed", "false");
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
  let managedCloseRequest = null;
  let tmuxCloseRequest = null;
  await page.route("**/api/services/web/sessions/96/close**", async (route) => {
    closeRequest = new URL(route.request().url());
    await route.fulfill({ json: { ok: true, message: "close SSH session ok" } });
  });
  await page.route("**/api/services/web/sessions/95/close**", async (route) => {
    managedCloseRequest = new URL(route.request().url());
    await route.fulfill({ json: { ok: true, message: "close managed SSH session ok" } });
  });
  await page.route("**/api/services/web/terminal-sessions/tmux-root/close**", async (route) => {
    tmuxCloseRequest = new URL(route.request().url());
    await route.fulfill({ json: { ok: true, message: "close terminal session ok" } });
  });

  const sessions = page.getByRole("table", { name: "Current SSH, tmux and screen sessions" });
  await expect(sessions).toContainText("root");
  await expect(sessions).toContainText("pts/11");
  await expect(sessions).toContainText("pts/0");
  await expect(sessions).toContainText("executable /usr/lib/sshd-session was replaced");
  const unavailableRow = sessions.locator("tr", { hasText: "pts/0" });
  const verifiedRow = sessions.locator("tr", { hasText: "pts/11" });
  await expect(unavailableRow.locator("td").nth(3)).toHaveText("95");
  await expect(unavailableRow.locator('[data-ssh-session-close]')).toHaveCount(1);
  await expect(verifiedRow.locator("td").nth(2)).not.toContainText("PID 96");
  await expect(verifiedRow.locator("td").nth(3)).toHaveText("96");
  await expect(sessions).toContainText("120s");
  await expect(sessions).toContainText("1 MiB");
  await expect(sessions).toContainText("1 KB/s / 250 B/s");
  await expect(sessions).toContainText("ops");
  await expect(sessions).not.toContainText("screen-root");
  await expect(sessions).toContainText("No active sessions");

  await verifiedRow.locator('[data-ssh-session-close]').click();
  await expect(page.locator("#simple-confirm")).toBeVisible();
  await page.locator("#simple-confirm-ok").click();
  await expect.poll(() => closeRequest && closeRequest.searchParams.get("terminal")).toBe("pts/11");
  expect(closeRequest.searchParams.get("start_ticks")).toBe("1234");
  expect(closeRequest.searchParams.has("managed_by_logind")).toBe(false);

  await unavailableRow.locator('[data-ssh-session-close]').click();
  await expect(page.locator("#simple-confirm-message")).toContainText("systemd-logind");
  await page.locator("#simple-confirm-ok").click();
  await expect.poll(() => managedCloseRequest && managedCloseRequest.searchParams.get("managed_by_logind")).toBe("true");
  expect(managedCloseRequest.searchParams.get("terminal")).toBe("pts/0");
  expect(managedCloseRequest.searchParams.get("start_ticks")).toBe("1200");

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

  await expect(page.locator('[data-sf="all"]')).toContainText("all 6");
  await expect(page.locator('[data-sf="ssh"]')).toContainText("ssh 1");
  await expect(page.locator('[data-sf="tmux"]')).toContainText("tmux 2");
  await expect(page.locator('[data-sf="screen"]')).toContainText("screen 1");
  await expect(page.locator("#session-rows")).toContainText("No active sessions");
  const emptySource = page.locator("#session-rows tr", { hasText: "tmux-empty" });
  const emptySourceCells = emptySource.locator("td");
  await expect(emptySourceCells.nth(4)).toHaveText("empty");
  await expect(emptySourceCells.nth(4).locator(".target-state")).toHaveClass(/state-empty/);
  await expect(emptySourceCells.nth(6)).toHaveText("—");
  await expect(emptySourceCells.nth(8)).toHaveText("—");
  // screen-root is empty but has no configured socket: no server to kill and
  // nothing to say, so it renders no row at all.
  await expect(page.locator("#session-rows tr", { hasText: "screen-root" })).toHaveCount(0);

  await emptySource.getByRole("button", { name: /^Close the empty tmux server/ }).click();
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

  const state = page.locator("#session-rows tr", { hasText: "pending" }).locator("td").nth(4);
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






// A row expansion spans every column of an auto-layout table, so its content's
// minimum width used to become the table's: on a phone the process table (a
// truncated command beside two usage bars) was wider than the viewport, the
// services table grew past its panel and the expanded row's action buttons hung
// off its right edge. An expansion must never widen the table it sits in.
test("expanding a service on a phone keeps its action buttons inside the table", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "mobile", "phone layout only");
  // With the host's memory known, each process row renders a memory bar next
  // to its CPU bar, the shape of a real host's process table.
  const body = JSON.parse(JSON.stringify(dashboard));
  body.host_metrics = [{ name: "total_memory", absolute: 536870912, total: 1073741824, percent: 50 }];
  await page.route("**/api/dashboard**", (route) => route.fulfill({ json: body }));
  await page.locator("#refresh-now").click();
  await expect(page.locator("#overview .tile", { hasText: "Host memory" })).toContainText("50%");
  const row = page.locator("#svc-row-web");
  await row.locator(".row-toggle").click();
  const detail = page.locator('[data-service-detail="web"]');
  await expect(detail.getByRole("table", { name: "Service processes" })).toBeVisible();

  const box = await page.evaluate(() => {
    const table = document.querySelector(".services-table");
    const cell = document.querySelector("#svc-row-web td.actions");
    const buttons = [...cell.querySelectorAll("button")];
    return {
      tableRight: table.getBoundingClientRect().right,
      panelRight: table.parentElement.getBoundingClientRect().right,
      cellRight: cell.getBoundingClientRect().right,
      buttonRight: Math.max(...buttons.map((b) => b.getBoundingClientRect().right)),
      scrollWidth: document.documentElement.scrollWidth,
      expansionOverflow: (() => { const b = document.querySelector('tr.exp-row[data-exp="svc:web"] > td > .exp-body'); return b.scrollWidth - b.clientWidth; })(),
    };
  });
  expect(box.tableRight).toBeLessThanOrEqual(box.panelRight + 1);
  expect(box.buttonRight).toBeLessThanOrEqual(box.cellRight + 1);
  expect(box.scrollWidth).toBeLessThanOrEqual(page.viewportSize().width + 1);
  // The phone-width bars let the process table fit the expansion outright, so
  // the wrapper's own scroll stays a last resort rather than the normal case.
  expect(box.expansionOverflow).toBeLessThanOrEqual(0);

  // The checks and rules tables are desk work: a phone hides both sections.
  await expect(detail.locator('[data-detail-section="checks"]')).toBeHidden();
  await expect(detail.locator('[data-detail-section="rules"]')).toBeHidden();
});

test("a check that graded its own failure a warning reads warn, not fail", async ({ page }) => {
  // The checks table is a desktop section; a phone hides it.
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.locator("#svc-row-web .row-toggle").click();
  const table = page.locator("#services-section .detail-checks-table");
  await expect(table).toBeVisible();
  const row = table.locator("tr", { hasText: "smart-sda" });
  await expect(row.locator(".inactive")).toHaveText("warn");
  await expect(row.locator(".bad")).toHaveCount(0);
});
