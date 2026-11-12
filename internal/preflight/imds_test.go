package preflight

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestIMDSContractAndOutcomes(t *testing.T) {
	for _, tc := range []struct {
		status  int
		outcome string
	}{{204, "accepted"}, {400, "rejected"}, {403, "rejected"}, {404, "rejected"}, {429, "rejected"}, {302, "rejected"}, {408, "uncertain"}, {500, "uncertain"}, {200, "uncertain"}} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			var calls atomic.Int32
			server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.URL.Path != "/v1/reallocate" || r.Header.Get("Metadata") != "true" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("wrong IMDS contract")
				}
				var body map[string]string
				if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || body["reason"] != "custom \"reason\" 界" {
					t.Error("invalid reason")
				}
				w.Header().Set("Location", "/must-not-follow")
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			client := server.Client
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got := postReallocation(ctx, client, server.URL+"/v1/reallocate", "custom \"reason\" 界")
			if got.Outcome != tc.outcome || got.Status != tc.status || calls.Load() != 1 {
				t.Fatal(got, calls.Load())
			}
		})
	}
}

func TestIMDSUncertainNeverRetried(t *testing.T) {
	for _, mode := range []string{"disconnect", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body any
				_ = json.NewDecoder(r.Body).Decode(&body)
				calls.Add(1)
				if mode == "disconnect" {
					conn, _, _ := w.(http.Hijacker).Hijack()
					conn.Close()
					return
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			client := server.Client
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			got := postReallocation(ctx, client, server.URL, "reason")
			if got.Outcome != "uncertain" || calls.Load() != 1 {
				t.Fatal(got, calls.Load())
			}
		})
	}
}

func TestMetadataTransportBypassesProxies(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:1")
	c, tr := directClient()
	defer tr.CloseIdleConnections()
	if tr.Proxy != nil || !tr.DisableKeepAlives || c.CheckRedirect == nil {
		t.Fatal("unsafe metadata transport")
	}
	if imdsURL != "http://169.254.169.254/v1/reallocate" {
		t.Fatal(imdsURL)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := postReallocation(ctx, c, "http://127.0.0.1:1", "reason")
	if got.Outcome != "not_sent" {
		t.Fatal(got)
	}
}
