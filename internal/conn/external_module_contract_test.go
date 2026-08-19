package conn

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const (
	externalModuleContractTimeout   = 2 * time.Second
	externalModuleContractBodyLimit = 64
)

// TestExternalModuleTransportContract is the common acceptance suite for
// third-party protocol adapters. A module must keep cancellation, interface
// binding, TLS policy, bounded reads and teardown under Sermo's ownership.
func TestExternalModuleTransportContract(t *testing.T) {
	t.Run("context deadline", testExternalModuleContextDeadline)
	t.Run("interface binding", testExternalModuleInterfaceBinding)
	t.Run("TLS policy", testExternalModuleTLSPolicy)
	t.Run("hostile response bound", testExternalModuleResponseBound)
	t.Run("cleanup", testExternalModuleCleanup)
}

func testExternalModuleContextDeadline(t *testing.T) {
	requestStarted := make(chan struct{})
	handlerDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
		close(handlerDone)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	client := externalModuleContractHTTPClient(t)
	_, err = doHTTPProbe(client, req, externalModuleContractBodyLimit)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("doHTTPProbe() error = %v, want context deadline", err)
	}
	select {
	case <-requestStarted:
	case <-time.After(externalModuleContractTimeout):
		t.Fatal("HTTP module request never reached the server")
	}
	select {
	case <-handlerDone:
	case <-time.After(externalModuleContractTimeout):
		t.Fatal("HTTP module handler remained blocked after context deadline")
	}
}

func testExternalModuleTLSPolicy(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	host, port := serverHostPort(t, srv)

	tests := []struct {
		name    string
		mode    string
		wantErr bool
	}{
		{name: "normal verification rejects untrusted certificate", mode: ParamValueTrue, wantErr: true},
		{name: "skip verify accepts untrusted certificate", mode: tlsSkipVerify},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, base := httpProbeBaseWithTLSMode(context.Background(), Config{Host: host, Port: port}, port, test.mode)
			closeHTTPClientOnCleanup(t, client)
			_, err := getHTTPProbe(context.Background(), client, base, externalModuleContractBodyLimit)
			if test.wantErr && err == nil {
				t.Fatal("strict TLS probe accepted an untrusted certificate")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("skip-verify TLS probe: %v", err)
			}
		})
	}
}

func testExternalModuleResponseBound(t *testing.T) {
	body := bytes.Repeat([]byte("x"), externalModuleContractBodyLimit*4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	client := externalModuleContractHTTPClient(t)
	response, err := getHTTPProbe(context.Background(), client, srv.URL, externalModuleContractBodyLimit)
	if err != nil {
		t.Fatalf("getHTTPProbe(): %v", err)
	}
	if len(response.body) != externalModuleContractBodyLimit {
		t.Fatalf("response body bytes = %d, want bounded %d", len(response.body), externalModuleContractBodyLimit)
	}
}

func testExternalModuleCleanup(t *testing.T) {
	handlerDone := make(chan struct{})
	connectionClosed := make(chan struct{})
	var closeOnce sync.Once
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
		close(handlerDone)
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			closeOnce.Do(func() { close(connectionClosed) })
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)

	client := externalModuleContractHTTPClient(t)
	transport := client.Transport.(*http.Transport)
	if !transport.DisableKeepAlives {
		t.Fatal("one-shot module transport must disable keep-alives")
	}
	if _, err := getHTTPProbe(context.Background(), client, srv.URL, externalModuleContractBodyLimit); err != nil {
		t.Fatalf("getHTTPProbe(): %v", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(externalModuleContractTimeout):
		t.Fatal("HTTP module handler did not return")
	}
	select {
	case <-connectionClosed:
	case <-time.After(externalModuleContractTimeout):
		t.Fatal("HTTP module connection remained open after the response closed")
	}
}

// externalModuleContractHTTPClient forces the custom-transport path without
// weakening TLS: the plain HTTP test URL does not use the supplied TLS config.
func externalModuleContractHTTPClient(t *testing.T) *http.Client {
	t.Helper()
	client := httpProbeClient("", &tls.Config{MinVersion: tls.VersionTLS12})
	closeHTTPClientOnCleanup(t, client)
	return client
}

func closeHTTPClientOnCleanup(t *testing.T, client *http.Client) {
	t.Helper()
	if transport, ok := client.Transport.(*http.Transport); ok {
		t.Cleanup(transport.CloseIdleConnections)
	}
}
