package preflight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGPUParsingAndReadOnlySelection(t *testing.T) {
	c := config(t, "GPU=GPU-test", "GPU_POWER_MIN_WATTS=100")
	for _, tc := range []struct {
		out   string
		err   error
		valid bool
	}{
		{"125.50\n", nil, true}, {"[N/A]\n", nil, false}, {"Not Supported", nil, false}, {"0", nil, false}, {"-1", nil, false}, {"NaN", nil, false}, {"Inf", nil, false}, {"100\n200", nil, false}, {"", errors.New("missing binary"), false},
	} {
		m := GPUProbe(context.Background(), c, c.Checks[2], func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name != "nvidia-smi" || strings.Join(args, " ") != "--id=GPU-test --query-gpu=power.limit --format=csv,noheader,nounits" {
				t.Fatal(name, args)
			}
			return []byte(tc.out), tc.err
		})
		if (m.Value != nil) != tc.valid {
			t.Fatal(tc, m)
		}
	}
	m := GPUProbe(context.Background(), c, c.Checks[1], func(context.Context, string, ...string) ([]byte, error) { return []byte("0"), nil })
	if m.Value == nil || *m.Value != 0 {
		t.Fatal("zero free memory is valid")
	}
	m = GPUProbe(context.Background(), c, c.Checks[0], func(context.Context, string, ...string) ([]byte, error) { return []byte("8192"), nil })
	if m.Value == nil || *m.Value != 8192 {
		t.Fatal(m)
	}
}

func TestGPUCommandMissingAndTimeout(t *testing.T) {
	c := config(t, "NVIDIA_SMI=/nonexistent/nvidia-smi")
	if m := GPUProbe(context.Background(), c, c.Checks[0], runCommand); m.Value != nil {
		t.Fatal(m)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runCommand(ctx, "/bin/sh", "-c", "sleep 30"); err == nil {
		t.Fatal("ignored cancellation")
	}
	if _, err := runCommand(context.Background(), "/bin/sh", "-c", "head -c 8192 /dev/zero"); err == nil {
		t.Fatal("unbounded stdout")
	}
}

func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestDownloadValidation(t *testing.T) {
	data := strings.Repeat("data", 1024)
	for _, kind := range []string{"valid", "html", "status", "digest", "truncated", "length", "encoding", "redirect", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer private-token" || r.Header.Get("Accept-Encoding") != "identity" {
					t.Error("invalid request")
				}
				if kind == "timeout" {
					<-r.Context().Done()
					return
				}
				if kind == "status" {
					http.Error(w, "error", 503)
					return
				}
				if kind == "redirect" {
					w.Header().Set("Location", "/unexpected")
					w.WriteHeader(302)
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Length", fmt.Sprint(len(data)))
				if kind == "html" {
					w.Header().Set("Content-Type", "text/html")
				}
				if kind == "encoding" {
					w.Header().Set("Content-Encoding", "gzip")
				}
				if kind == "length" {
					w.Header().Set("Content-Length", "3")
					io.WriteString(w, "bad")
					return
				}
				if kind == "truncated" {
					io.WriteString(w, data[:10])
					return
				}
				if kind == "digest" {
					io.WriteString(w, strings.Repeat("x", len(data)))
					return
				}
				io.WriteString(w, data)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			m := networkProbe(ctx, Network{URL: server.URL, Token: "private-token", Bytes: int64(len(data)), SHA256: digest(data)}, false, server.Client)
			if (m.Value != nil) != (kind == "valid") {
				t.Fatal(m)
			}
			if m.Value != nil && *m.Value <= 0 {
				t.Fatal(m)
			}
		})
	}
}

func TestUploadBoundedGeneratedDataAndAcknowledgement(t *testing.T) {
	for _, kind := range []string{"valid", "wrong_count", "html", "oversize", "status", "extra_json", "disconnect", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			var requests atomic.Int32
			server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != "POST" || r.ContentLength != 8192 || r.Header.Get("Content-Type") != "application/octet-stream" {
					t.Error("invalid upload request")
				}
				b, err := io.ReadAll(io.LimitReader(r.Body, 8193))
				if err != nil || len(b) != 8192 {
					t.Error("invalid payload length")
				}
				if kind == "disconnect" {
					conn, _, _ := w.(http.Hijacker).Hijack()
					conn.Close()
					return
				}
				if kind == "timeout" {
					<-r.Context().Done()
					return
				}
				if kind == "status" {
					w.WriteHeader(500)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if kind == "html" {
					w.Header().Set("Content-Type", "text/html")
					io.WriteString(w, "error page")
					return
				}
				if kind == "oversize" {
					io.WriteString(w, strings.Repeat(" ", 5000))
					return
				}
				if kind == "wrong_count" {
					io.WriteString(w, `{"received_bytes":1}`)
					return
				}
				io.WriteString(w, `{"received_bytes":8192}`)
				if kind == "extra_json" {
					io.WriteString(w, `{}`)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			m := networkProbe(ctx, Network{URL: server.URL, Bytes: 8192}, true, server.Client)
			if (m.Value != nil) != (kind == "valid") || requests.Load() != 1 {
				t.Fatal(m, requests.Load())
			}
		})
	}
}

func TestEndpointTransientRecovery(t *testing.T) {
	var calls atomic.Int32
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "4")
		io.WriteString(w, "data")
	}))
	defer server.Close()
	c := config(t, "DOWNLOAD_MIN_MBPS=0.000001", "DOWNLOAD_URL="+server.URL, "DOWNLOAD_BYTES=4", "DOWNLOAD_SHA256="+digest("data"), "RETRY_DELAY=0s")
	got := evaluate(context.Background(), c, c.Lookup("download"), func(ctx context.Context, _ Check) Measurement {
		return networkProbe(ctx, c.Download, false, server.Client)
	}, func(Event) {})
	if got != "pass" || calls.Load() != 2 {
		t.Fatal(got, calls.Load())
	}
}

const smiSummary = `Wed Nov 11 10:00:00 2026
+-----------------------------------------------------------------------------------------+
| NVIDIA-SMI 561.19                 Driver Version: 561.19         CUDA Version: 12.6     |
|-----------------------------------------+------------------------+----------------------+
| GPU  Name                 Persistence-M | Bus-Id          Disp.A | Volatile Uncorr. ECC |
|   0  NVIDIA GeForce RTX 3080 Ti     Off |   00000000:01:00.0  On |                  N/A |
+-----------------------------------------------------------------------------------------+
`

func TestGPUVersionsAndMaximumReadings(t *testing.T) {
	c := config(t)
	for _, tc := range []struct {
		check, out, args string
		value            float64
		text             string
		valid            bool
	}{
		{"gpu_driver", "561.19\n", "--id=0 --query-gpu=driver_version --format=csv,noheader,nounits", 561019000, "561.19", true},
		{"gpu_driver", "550.54.14", "--id=0 --query-gpu=driver_version --format=csv,noheader,nounits", 550054014, "550.54.14", true},
		{"gpu_driver", "[N/A]", "", 0, "", false},
		{"gpu_driver", "32.0.15.6119", "", 0, "", false},
		{"gpu_driver", "561.19\n561.19", "", 0, "", false},
		{"gpu_cuda", smiSummary, "--id=0", 12006000, "12.6", true},
		{"gpu_cuda", "| NVIDIA-SMI 580.65  Driver Version: 580.65  CUDA Version: 12.10 |", "--id=0", 12010000, "12.10", true},
		{"gpu_cuda", "CUDA Version:13", "--id=0", 13000000, "13", true},
		// Captured on the Salad validation node with the 610-series driver.
		{"gpu_cuda", "| NVIDIA-SMI 610.57.01              KMD Version: 610.88        CUDA UMD Version: 13.3     |", "--id=0", 13003000, "13.3", true},
		{"gpu_cuda", "CUDA UMD Version: N/A", "", 0, "", false},
		{"gpu_cuda", "CUDA UMD Version: 13.3invalid", "", 0, "", false},
		{"gpu_cuda", "CUDA Version: 12.6invalid", "", 0, "", false},
		{"gpu_cuda", "CUDA UMD Version: N/A | CUDA Version: 12.6", "", 0, "", false},
		{"gpu_cuda", strings.Replace(smiSummary, "12.6", "N/A", 1), "", 0, "", false},
		{"gpu_cuda", "| NVIDIA-SMI 390.25    Driver Version: 390.25 |", "", 0, "", false},
		{"gpu_cuda", "CUDA Version: 12.6.", "", 0, "", false},
		{"gpu_cuda", "", "", 0, "", false},
		{"gpu_utilization", "0", "--id=0 --query-gpu=utilization.gpu --format=csv,noheader,nounits", 0, "", true},
		{"gpu_utilization", "37\n", "", 37, "", true},
		{"gpu_utilization", "100", "", 100, "", true},
		{"gpu_utilization", "101", "", 0, "", false},
		{"gpu_utilization", "[N/A]", "", 0, "", false},
		{"gpu_temperature", "65", "--id=0 --query-gpu=temperature.gpu --format=csv,noheader,nounits", 65, "", true},
		{"gpu_temperature", "0", "", 0, "", false},
		{"gpu_temperature", "4294967295", "", 0, "", false},
		{"gpu_temperature", "N/A", "", 0, "", false},
	} {
		m := GPUProbe(context.Background(), c, c.Lookup(tc.check), func(_ context.Context, name string, args ...string) ([]byte, error) {
			if got := strings.Join(args, " "); name != "nvidia-smi" || (tc.args != "" && got != tc.args) {
				t.Fatal(name, got)
			}
			return []byte(tc.out), nil
		})
		if (m.Value != nil) != tc.valid || (tc.valid && (*m.Value != tc.value || m.Text != tc.text)) || (!tc.valid && m.Detail != "gpu_telemetry_unavailable") {
			t.Fatalf("%+v: %+v", tc, m)
		}
	}
	if m := GPUProbe(context.Background(), c, c.Lookup("gpu_cuda"), func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("missing") }); m.Detail != "gpu_tool_error" {
		t.Fatal(m)
	}
}

func TestBenchmarkOutputContract(t *testing.T) {
	for _, tc := range []struct {
		out   string
		err   error
		value float64
		valid bool
	}{
		{"42.5\n", nil, 42.5, true}, {"  7 ", nil, 7, true}, {"0", nil, 0, true}, {"1e3", nil, 1000, true},
		{"fast", nil, 0, false}, {"", nil, 0, false}, {"1\n2", nil, 0, false}, {"score: 5", nil, 0, false},
		{"-1", nil, 0, false}, {"NaN", nil, 0, false}, {"Inf", nil, 0, false}, {"99", errors.New("exit status 1"), 0, false},
	} {
		m := BenchmarkProbe(context.Background(), "/opt/bench", func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "/opt/bench" || len(args) != 0 {
				t.Fatal(name, args)
			}
			return []byte(tc.out), tc.err
		})
		if (m.Value != nil) != tc.valid || (tc.valid && *m.Value != tc.value) {
			t.Fatalf("%+v: %+v", tc, m)
		}
	}
}

func TestBenchmarkRunsExecutableAndDeadlineStopsItsChildren(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc to observe the child process")
	}
	dir := t.TempDir()
	quick := filepath.Join(dir, "quick")
	if err := os.WriteFile(quick, []byte("#!/bin/sh\necho 'noise' >&2\necho 12.5\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if m := BenchmarkProbe(context.Background(), quick, runCommand); m.Value == nil || *m.Value != 12.5 {
		t.Fatal(m)
	}
	pidFile := filepath.Join(dir, "pid")
	t.Setenv("PREFLIGHT_TEST_PIDFILE", pidFile)
	slow := filepath.Join(dir, "slow")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nsleep 300 &\necho $! > \"$PREFLIGHT_TEST_PIDFILE.tmp\"\nmv \"$PREFLIGHT_TEST_PIDFILE.tmp\" \"$PREFLIGHT_TEST_PIDFILE\"\nwait\necho 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Measurement, 1)
	go func() { done <- BenchmarkProbe(ctx, slow, runCommand) }()
	var pid string
	for deadline := time.Now().Add(10 * time.Second); pid == "" && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if b, err := os.ReadFile(pidFile); err == nil {
			pid = strings.TrimSpace(string(b))
		}
	}
	if pid == "" {
		t.Fatal("benchmark never started its child")
	}
	cancel() // stands in for the per-attempt deadline
	if m := <-done; m.Value != nil || m.Detail != "benchmark_error" {
		t.Fatal(m)
	}
	// The grandchild must be gone (or a zombie awaiting its reaper), not still running.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		b, err := os.ReadFile("/proc/" + pid + "/stat")
		if err != nil {
			return
		}
		if i := strings.LastIndex(string(b), ") "); i >= 0 && strings.HasPrefix(string(b)[i+2:], "Z") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("benchmark child survived cancellation")
		}
	}
}

type fakeConn struct {
	net.Conn
	remote net.Addr
}

func (c fakeConn) RemoteAddr() net.Addr { return c.remote }
func (c fakeConn) Close() error         { return nil }

func TestLatencyIsMedianConnectTimeToOnePinnedAddress(t *testing.T) {
	// First dial is the untimed warm-up; two slow outliers must not move the median.
	delays := []time.Duration{80 * time.Millisecond, time.Millisecond, 60 * time.Millisecond, 3 * time.Millisecond, 60 * time.Millisecond, 2 * time.Millisecond}
	var addresses []string
	dial := func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" {
			t.Fatal(network)
		}
		time.Sleep(delays[len(addresses)])
		addresses = append(addresses, address)
		return fakeConn{remote: &net.TCPAddr{IP: net.ParseIP("192.0.2.7"), Port: 443}}, nil
	}
	m := latencyProbe(context.Background(), "storage.example.com:443", 5, dial)
	if m.Value == nil || *m.Value < 3 || *m.Value >= 20 {
		t.Fatal(m)
	}
	if got := strings.Join(addresses, " "); got != "storage.example.com:443 192.0.2.7:443 192.0.2.7:443 192.0.2.7:443 192.0.2.7:443 192.0.2.7:443" {
		t.Fatal(got)
	}
	calls := 0
	even := latencyProbe(context.Background(), "host:1", 2, func(context.Context, string, string) (net.Conn, error) {
		calls++
		return fakeConn{remote: pipeAddr("memory")}, nil
	})
	if even.Value == nil || *even.Value < 0 || calls != 3 {
		t.Fatal(even, calls)
	}
}

func TestLatencyEndpointProblemsAreUnknown(t *testing.T) {
	for _, failAt := range []int{1, 3} {
		calls := 0
		m := latencyProbe(context.Background(), "host:443", 5, func(context.Context, string, string) (net.Conn, error) {
			if calls++; calls == failAt {
				return nil, errors.New("connection refused by secret-host")
			}
			return fakeConn{remote: pipeAddr("memory")}, nil
		})
		if m.Value != nil || m.Detail != "endpoint_error" || calls != failAt {
			t.Fatal(m, calls)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	m := latencyProbe(ctx, "host:443", 5, func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if m.Value != nil {
		t.Fatal(m)
	}
	c := config(t, "LATENCY_MAX_MS=50", "LATENCY_ADDRESS=host:443", "RETRY_DELAY=0s")
	got := evaluate(context.Background(), c, c.Lookup("latency"), func(ctx context.Context, _ Check) Measurement {
		return latencyProbe(ctx, c.LatencyAddress, 1, func(context.Context, string, string) (net.Conn, error) {
			time.Sleep(60 * time.Millisecond)
			return fakeConn{remote: pipeAddr("memory")}, nil
		})
	}, func(Event) {})
	if got != "fail" {
		t.Fatal(got)
	}
}
