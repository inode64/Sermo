package app

import "testing"

func TestCheckReadingsCertAndCount(t *testing.T) {
	cert := checkReadings("cert", map[string]any{
		"source":    "/etc/ssl/cert.pem",
		"days_left": 30,
		"not_after": "2026-12-31T00:00:00Z",
		"issuer":    "Test CA",
		"dns_names": []string{"example.com", "www.example.com"},
	})
	if len(cert) < 4 {
		t.Fatalf("cert readings = %+v", cert)
	}
	count := checkReadings("count", map[string]any{
		"path":  "/var/log",
		"of":    "file",
		"count": 12,
	})
	if readingByField(count, "count").Value != "12" {
		t.Fatalf("count reading = %+v", count)
	}
}

func TestCheckReadingsFirewallAndFile(t *testing.T) {
	fw := checkReadings("firewall_rules", map[string]any{
		"backend":   "nftables",
		"rules":     uint64(99),
		"min_rules": 1,
	})
	if readingByField(fw, "rules").Value != "99" {
		t.Fatalf("firewall readings = %+v", fw)
	}
	file := checkReadings("file", map[string]any{"path": "/etc/hosts", "size": int64(220)})
	if readingByField(file, "size").Value != "220 B" {
		t.Fatalf("file readings = %+v", file)
	}
}

func TestCheckReadingsConnHTTPAndResource(t *testing.T) {
	tcp := checkReadings("tcp", map[string]any{
		"host": "127.0.0.1", "port": 443, "latency_ms": int64(12), "protocol": "tcp",
	})
	if readingByField(tcp, "latency_ms").Value != "12 ms" {
		t.Fatalf("tcp readings = %+v", tcp)
	}
	http := checkReadings("http", map[string]any{"status": 200, "latency_ms": int64(45)})
	if readingByField(http, "status").Value != "200" || readingByField(http, "latency_ms").Value != "45 ms" {
		t.Fatalf("http readings = %+v", http)
	}
	storage := checkReadings("storage", map[string]any{
		"path": "/", "used_pct": 88.5, "free_bytes": uint64(1 << 30),
	})
	if readingByField(storage, "used_pct").Value != "88.50%" {
		t.Fatalf("storage readings = %+v", storage)
	}
	pressure := checkReadings("pressure", map[string]any{"some_avg60": 2.5, "value": 2.5})
	if readingByField(pressure, "some_avg60").Value != "2.50%" {
		t.Fatalf("pressure readings = %+v", pressure)
	}
	diskio := checkReadings("diskio", map[string]any{"device": "sda", "util_pct": 50.0, "read_bytes": 1024.0})
	if readingByField(diskio, "util_pct").Value != "50.00%" {
		t.Fatalf("diskio readings = %+v", diskio)
	}
}
