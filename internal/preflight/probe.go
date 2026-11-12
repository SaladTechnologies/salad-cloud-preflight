package preflight

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type Measurement struct {
	Value                           *float64
	ObservedAmount, ReferenceAmount *float64 // raw quantities behind a percentage
	Text                            string   // readable form when Value is an encoded version
	Detail                          string
}

func measured(v float64) Measurement    { return Measurement{Value: &v} }
func unknown(detail string) Measurement { return Measurement{Detail: detail} }

type Probe func(context.Context, Check) Measurement
type Command func(context.Context, string, ...string) ([]byte, error)

// boundedOutput prevents unexpected utility output from exhausting memory.
type boundedOutput struct {
	b     []byte
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(b.b)+len(p) > b.limit {
		return 0, fmt.Errorf("output limit")
	}
	b.b = append(b.b, p...)
	return len(p), nil
}

func commandWithLimit(limit int) Command {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		out := boundedOutput{limit: limit}
		cmd.Stdout = &out
		cmd.Stderr = io.Discard
		cmd.WaitDelay = 100 * time.Millisecond
		isolate(cmd)
		err := cmd.Run()
		return out.b, err
	}
}

var (
	runCommand = commandWithLimit(4096)
	// The CUDA version exists only in nvidia-smi's summary table, which also
	// lists processes and so needs more room than a single queried field.
	runSummaryCommand = commandWithLimit(64 * 1024)
)

func GPUProbe(ctx context.Context, c Config, check Check, command Command) Measurement {
	args := []string{"--id=" + c.GPU}
	if check.Field != "" {
		// Query fields separately: unsupported power telemetry must not invalidate VRAM.
		fields := check.Field
		if check.ReferenceField != "" {
			fields += "," + check.ReferenceField
		}
		args = append(args, "--query-gpu="+fields, "--format=csv,noheader,nounits")
	}
	out, err := command(ctx, c.SMI, args...)
	if err != nil {
		return unknown("gpu_tool_error")
	}
	text := strings.TrimSpace(string(out))
	if check.ReferenceField != "" {
		parts := strings.Split(text, ",")
		if len(parts) != 2 || strings.ContainsAny(text, "\r\n") {
			return unknown("gpu_telemetry_unavailable")
		}
		observed, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		reference, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err1 != nil || err2 != nil || math.IsNaN(observed) || math.IsInf(observed, 0) || math.IsNaN(reference) || math.IsInf(reference, 0) || reference <= 0 || observed < 0 || (!check.ZeroValid && observed == 0) || (check.ReferenceField == "memory.total" && observed > reference) {
			return unknown("gpu_telemetry_unavailable")
		}
		percent := observed / reference * 100
		if math.IsNaN(percent) || math.IsInf(percent, 0) {
			return unknown("gpu_telemetry_unavailable")
		}
		return Measurement{Value: &percent, ObservedAmount: &observed, ReferenceAmount: &reference}
	}
	if check.Field == "" {
		// No query field reports the CUDA version; read it from the summary header.
		// "N/A" or a missing header leaves text empty, which is not a version.
		// New NVIDIA utilities renamed CUDA Version to CUDA UMD Version.
		// Both report the newest CUDA version supported by the driver.
		_, rest, found := strings.Cut(text, "CUDA UMD Version:")
		if !found {
			_, rest, _ = strings.Cut(text, "CUDA Version:")
		}
		rest = strings.TrimLeft(rest, " \t")
		end := strings.IndexAny(rest, " \t\r\n|")
		if end < 0 {
			end = len(rest)
		}
		text = rest[:end]
	}
	if check.Version {
		v, ok := parseVersion(text)
		if !ok || v == 0 {
			return unknown("gpu_telemetry_unavailable")
		}
		return Measurement{Value: &v, Text: text}
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || (!check.ZeroValid && v == 0) || (check.Ceiling > 0 && v > check.Ceiling) {
		return unknown("gpu_telemetry_unavailable")
	}
	return measured(v)
}

// BenchmarkProbe runs the customer's executable, which must print one number.
// A failed run or any other output is unknown; a printed number is a measurement.
func BenchmarkProbe(ctx context.Context, executable string, command Command) Measurement {
	out, err := command(ctx, executable)
	if err != nil {
		return unknown("benchmark_error")
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return unknown("benchmark_output_invalid")
	}
	return measured(v)
}

type Dial func(ctx context.Context, network, address string) (net.Conn, error)

func LatencyProbe(ctx context.Context, address string, samples int) Measurement {
	return latencyProbe(ctx, address, samples, (&net.Dialer{}).DialContext)
}

// latencyProbe reports the median time to open a TCP connection, which is about
// one network round trip. No data is sent. A first, untimed connection resolves
// DNS and pins one address, so name lookup and a cold route are not measured.
func latencyProbe(ctx context.Context, address string, samples int, dial Dial) Measurement {
	conn, err := dial(ctx, "tcp", address)
	if err != nil {
		return unknown("endpoint_error")
	}
	if tcp, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		address = tcp.String()
	}
	conn.Close()
	times := make([]float64, 0, samples)
	for len(times) < samples {
		start := time.Now()
		conn, err := dial(ctx, "tcp", address)
		elapsed := time.Since(start)
		if err != nil {
			return unknown("endpoint_error")
		}
		conn.Close()
		times = append(times, float64(elapsed)/float64(time.Millisecond))
	}
	sort.Float64s(times)
	median := times[len(times)/2]
	if len(times)%2 == 0 {
		median = (times[len(times)/2-1] + median) / 2
	}
	return measured(median)
}

// Each probe has a fresh direct transport. No redirects, proxy credentials,
// compression, or automatic retries of a possibly successful upload.
func directClient() (*http.Client, *http.Transport) {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DisableCompression = true
	t.DisableKeepAlives = true
	t.MaxResponseHeaderBytes = 16 * 1024
	return &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, t
}

func mediaType(resp *http.Response, expected string) bool {
	t, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return err == nil && t == expected && resp.Header.Get("Content-Encoding") == ""
}

type uploadReader struct {
	remaining io.Reader
	sent      atomic.Int64
}

func (r *uploadReader) Read(p []byte) (int, error) {
	n, err := r.remaining.Read(p)
	r.sent.Add(int64(n))
	return n, err
}

func NetworkProbe(ctx context.Context, n Network, upload bool) Measurement {
	client, transport := directClient()
	defer transport.CloseIdleConnections()
	return networkProbe(ctx, n, upload, client)
}

func networkProbe(ctx context.Context, n Network, upload bool, client *http.Client) Measurement {
	method := http.MethodGet
	var body io.Reader
	payload := &uploadReader{remaining: io.LimitReader(rand.Reader, n.Bytes)}
	if upload {
		method = http.MethodPost
		body = payload
	}
	req, err := http.NewRequestWithContext(ctx, method, n.URL, body)
	if err != nil {
		return unknown("request_error")
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-cache, no-store")
	if n.Token != "" {
		req.Header.Set("Authorization", "Bearer "+n.Token)
	}
	if upload {
		req.ContentLength = n.Bytes
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Accept", "application/json")
	} else {
		req.Header.Set("Accept", "application/octet-stream")
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return unknown("endpoint_error")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return unknown("endpoint_status")
	}
	if upload {
		if !mediaType(resp, "application/json") {
			return unknown("invalid_upload_response")
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
		if err != nil || len(data) > 4096 {
			return unknown("invalid_upload_response")
		}
		var ack struct {
			ReceivedBytes int64 `json:"received_bytes"`
		}
		if json.Unmarshal(data, &ack) != nil || ack.ReceivedBytes != n.Bytes || payload.sent.Load() != n.Bytes {
			return unknown("invalid_upload_acknowledgement")
		}
	} else {
		if !mediaType(resp, "application/octet-stream") || resp.ContentLength != n.Bytes {
			return unknown("invalid_download_response")
		}
		h := sha256.New()
		read, err := io.CopyBuffer(h, io.LimitReader(resp.Body, n.Bytes+1), make([]byte, 32*1024))
		if err != nil || read != n.Bytes {
			return unknown("incomplete_download")
		}
		if hex.EncodeToString(h.Sum(nil)) != n.SHA256 {
			return unknown("download_digest_mismatch")
		}
	}
	if ctx.Err() != nil {
		return unknown("probe_timeout")
	}
	return measured(float64(n.Bytes) * 8 / time.Since(start).Seconds() / 1e6)
}

func NewProbe(c Config) Probe {
	return func(ctx context.Context, check Check) Measurement {
		switch check.Name {
		case "download":
			return NetworkProbe(ctx, c.Download, false)
		case "upload":
			return NetworkProbe(ctx, c.Upload, true)
		case "latency":
			return LatencyProbe(ctx, c.LatencyAddress, c.LatencySamples)
		case "benchmark":
			return BenchmarkProbe(ctx, c.BenchmarkCommand, runCommand)
		case "gpu_cuda":
			return GPUProbe(ctx, c, check, runSummaryCommand)
		default:
			return GPUProbe(ctx, c, check, runCommand)
		}
	}
}
