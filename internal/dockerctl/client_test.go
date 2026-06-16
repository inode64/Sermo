package dockerctl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientInfoInspectAndStop(t *testing.T) {
	var stopped bool
	mux := http.NewServeMux()
	mux.HandleFunc("/info", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"Containers":2,"ContainersRunning":1,"Images":4,"ServerVersion":"25.0.0","Warnings":["x"]}`))
	})
	mux.HandleFunc("/containers/web/json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"Name":"/web","RestartCount":3,"State":{"Status":"running","Running":true,"Pid":1234,"Health":{"Status":"healthy"}}}`))
	})
	mux.HandleFunc("/containers/web/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") != "-1" {
			t.Errorf("stop timeout = %q, want -1", r.URL.Query().Get("t"))
		}
		stopped = true
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := &Client{HTTP: srv.Client(), Base: srv.URL}
	info, err := client.Info(context.Background())
	if err != nil {
		t.Fatalf("Info() error = %v", err)
	}
	if info.ServerVersion != "25.0.0" || info.ContainersRunning != 1 || len(info.Warnings) != 1 {
		t.Fatalf("Info() = %+v", info)
	}
	container, err := client.Inspect(context.Background(), "web")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if container.ContainerName() != "web" || container.HealthStatus() != "healthy" || container.State.Pid != 1234 {
		t.Fatalf("Inspect() = %+v", container)
	}
	if err := client.Stop(context.Background(), "web"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !stopped {
		t.Fatal("Stop() did not call Docker stop endpoint")
	}
}

func TestClientHTTPStatusError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/info", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"missing"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := &Client{HTTP: srv.Client(), Base: srv.URL}
	_, err := client.Info(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("Info() error = %v, want HTTP 404", err)
	}
}

func TestSpecFromTreeDockerDefaults(t *testing.T) {
	spec, ok, err := SpecFromTree(map[string]any{"control": map[string]any{
		"type":      "docker",
		"container": "web",
	}})
	if err != nil || !ok {
		t.Fatalf("SpecFromTree() ok=%v err=%v", ok, err)
	}
	if spec.Socket != DefaultSocket || spec.Port != DefaultPort || spec.Container != "web" {
		t.Fatalf("SpecFromTree() = %+v", spec)
	}
}

func TestSpecFromTreeDockerRejectsUnsafeOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		tree map[string]any
	}{
		{name: "missing container", tree: map[string]any{"type": "docker"}},
		{name: "socket and host", tree: map[string]any{"type": "docker", "container": "web", "socket": "/run/docker.sock", "host": "127.0.0.1"}},
		{name: "relative socket", tree: map[string]any{"type": "docker", "container": "web", "socket": "docker.sock"}},
		{name: "bad port", tree: map[string]any{"type": "docker", "container": "web", "host": "127.0.0.1", "port": 70000}},
		{name: "interface", tree: map[string]any{"type": "docker", "container": "web", "interface": "eth0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok, err := SpecFromTree(map[string]any{"control": tc.tree})
			if !ok || err == nil {
				t.Fatalf("SpecFromTree() ok=%v err=%v, want error", ok, err)
			}
		})
	}
}
