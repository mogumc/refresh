package process

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPReadiness(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" || r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	p := &Process{ReadyHTTP: server.URL + "/ready", ReadyTimeout: time.Second, ReadyInterval: 10 * time.Millisecond}
	if err := NewProcessManager().waitUntilReady(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if calls.Load() < 3 {
		t.Fatal("returned before endpoint was ready")
	}
}

func TestHTTPReadinessFailures(t *testing.T) {
	for _, mode := range []string{"status", "redirect", "hanging", "cancel", "exit"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "redirect":
					if r.URL.Path == "/ok" {
						w.WriteHeader(http.StatusOK)
						return
					}
					http.Redirect(w, r, "/ok", http.StatusFound)
				case "hanging", "cancel", "exit":
					if mode == "cancel" {
						cancel()
					}
					if mode == "exit" {
						close(done)
					}
					<-r.Context().Done()
				default:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			defer server.Close()
			p := &Process{ReadyHTTP: server.URL, ReadyTimeout: 100 * time.Millisecond, ReadyInterval: time.Second, done: done}
			started := time.Now()
			err := NewProcessManager().waitUntilReady(ctx, p)
			if err == nil {
				t.Fatal("expected readiness failure")
			}
			if time.Since(started) > 750*time.Millisecond {
				t.Fatal("probe did not stop promptly")
			}
			switch mode {
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case "exit":
				if !strings.Contains(err.Error(), "exited before") {
					t.Fatal(err)
				}
			default:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestReadinessValidation(t *testing.T) {
	for _, check := range []Readiness{
		{}, {HTTP: "/ready"}, {HTTP: "ftp://localhost/ready"}, {HTTP: "http:///ready"},
		{HTTP: "http://localhost/#ready"}, {HTTP: "http://localhost", TCP: "localhost:80"},
		{HTTP: "http://localhost", Timeout: "0s"}, {HTTP: "http://localhost", Interval: "-1s"},
	} {
		if err := (Execute{Cmd: "app", Readiness: &check}).Validate(); err == nil {
			t.Errorf("accepted invalid readiness: %+v", check)
		}
	}
	for _, address := range []string{"http://localhost:5173/ready", "https://example.com/health?full=true"} {
		if err := (Execute{Cmd: "app", Readiness: &Readiness{HTTP: address}}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
