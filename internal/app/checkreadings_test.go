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
