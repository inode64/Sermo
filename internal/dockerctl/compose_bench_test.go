package dockerctl

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func BenchmarkComposeDiscoveryOwnership(b *testing.B) {
	const count = 1000
	containers := make([]ContainerSummary, count)
	managers := make([]Manager, count)
	for i := range containers {
		name := fmt.Sprintf("app-%d", i)
		containers[i] = ContainerSummary{Names: []string{"/" + name}, Labels: map[string]string{composeProjectLabel: name, composeServiceLabel: "app"}}
		managers[i] = Manager{Spec: Spec{Container: name, Compose: &ComposeSpec{Project: name, Service: "app"}}}
	}
	body, err := json.Marshal(containers)
	if err != nil {
		b.Fatal(err)
	}
	var listings atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		listings.Add(1)
		_, _ = w.Write(body)
	}))
	b.Cleanup(server.Close)
	client := &Client{HTTP: server.Client(), Base: server.URL}
	b.Cleanup(client.CloseIdleConnections)
	b.ResetTimer()
	for b.Loop() {
		inventory, err := client.ListContainers(b.Context(), true)
		if err != nil {
			b.Fatal(err)
		}
		for _, manager := range managers {
			if err := manager.checkComposeReplicas(b.Context(), client, inventory); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.ReportMetric(count, "containers")
	b.ReportMetric(float64(listings.Load())/float64(b.N), "listings/op")
}
