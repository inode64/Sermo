package conn

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
)

func dockerMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/info", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"Containers":5,"ContainersRunning":3,"ContainersPaused":1,"ContainersStopped":1,"Images":12,"ServerVersion":"24.0.5","Warnings":["No swap limit support"]}`))
	})
	mux.HandleFunc("/containers/web/json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"Name":"/web","RestartCount":2,"State":{"Status":"running","Running":true,"ExitCode":0,"Health":{"Status":"healthy"}}}`))
	})
	mux.HandleFunc("/containers/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No such container"}`))
	})
	return mux
}

func probeDocker(t *testing.T, serverURL string, cfg Config) (Result, error) {
	t.Helper()
	u, _ := url.Parse(serverURL)
	port, _ := strconv.Atoi(u.Port())
	cfg.Host, cfg.Port = u.Hostname(), port
	return dockerProtocol{}.Probe(context.Background(), cfg)
}

func TestDockerInfoCounts(t *testing.T) {
	srv := httptest.NewServer(dockerMux())
	defer srv.Close()
	res, err := probeDocker(t, srv.URL, Config{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "24.0.5" {
		t.Errorf("version = %q, want 24.0.5", res.Version)
	}
	want := map[string]string{
		"containers": "5", "containers.running": "3", "containers.paused": "1",
		"containers.stopped": "1", "images": "12", "warnings": "1",
	}
	for k, v := range want {
		if res.Extra[k] != v {
			t.Errorf("%s = %q, want %q", k, res.Extra[k], v)
		}
	}
}

func TestDockerContainerState(t *testing.T) {
	srv := httptest.NewServer(dockerMux())
	defer srv.Close()
	res, err := probeDocker(t, srv.URL, Config{Query: "web"})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	for k, v := range map[string]string{
		"container": "web", "container.status": "running", "container.health": "healthy",
		"container.running": "true", "container.restartcount": "2", "fingerprint": "running/healthy",
	} {
		if res.Extra[k] != v {
			t.Errorf("%s = %q, want %q", k, res.Extra[k], v)
		}
	}
}

func TestDockerUnknownContainer(t *testing.T) {
	srv := httptest.NewServer(dockerMux())
	defer srv.Close()
	if _, err := probeDocker(t, srv.URL, Config{Query: "ghost"}); err == nil {
		t.Fatal("an unknown container must fail the probe")
	}
}

func TestDockerOverUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	srv := &http.Server{Handler: dockerMux()} //nolint:gosec // test server, no timeouts needed
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	res, err := dockerProtocol{}.Probe(context.Background(), Config{Socket: sock})
	if err != nil {
		t.Fatalf("probe over socket: %v", err)
	}
	if res.Extra["containers.running"] != "3" {
		t.Fatalf("containers.running = %q, want 3", res.Extra["containers.running"])
	}
}
