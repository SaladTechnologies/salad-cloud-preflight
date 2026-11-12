package preflight

import (
	"strings"
	"testing"
	"time"
)

func config(t *testing.T, entries ...string) Config {
	t.Helper()
	env := make([]string, len(entries))
	for i, e := range entries {
		env[i] = Prefix + e
	}
	c, err := Parse(env)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDefaultsAndIndependentEnablement(t *testing.T) {
	c := config(t)
	if !c.DryRun || c.UnknownPolicy != "continue" {
		t.Fatal(c)
	}
	for _, ch := range c.Checks {
		if ch.Enabled {
			t.Fatal(ch)
		}
	}
	c = config(t, "GPU_TOTAL_MIN_MIB=8192", "GPU_FREE_MIN_MIB=1024", "GPU_FREE_ENABLED=false", "GPU_POWER_MIN_WATTS=100", "GPU_POWER_REASON=custom ceiling")
	if !c.Checks[0].Enabled || c.Checks[1].Enabled || !c.Checks[2].Enabled || c.Checks[2].Reason != "custom ceiling" {
		t.Fatal(c)
	}
}

func TestInvalidConfiguration(t *testing.T) {
	for _, entries := range [][]string{
		{"DRY_RUN=yes"}, {"TYPO=true"}, {"GPU_TOTAL_ENABLED=true"},
		{"GPU_TOTAL_MIN_MIB=NaN"}, {"GPU_FREE_MIN_MIB=Inf"}, {"GPU_POWER_MIN_WATTS=-1"},
		{"GPU_TOTAL_MIN_MIB=0"}, {"GPU_TOTAL_ENABLED=false", "GPU_TOTAL_MIN_MIB=bad"},
		{"DOWNLOAD_MIN_MBPS=5"}, {"UPLOAD_MIN_MBPS=5"},
		{"DOWNLOAD_URL=file:///tmp/secret"}, {"UPLOAD_URL=https://user:secret@example.com"},
		{"UPLOAD_URL=http://example.com/#secret"}, {"DOWNLOAD_SHA256=xyz"},
		{"DOWNLOAD_BYTES=0"}, {"UPLOAD_BYTES=1073741825"}, {"ATTEMPTS=11"},
		{"ATTEMPTS=1"}, {"FAIL_CONFIRMATIONS=0"}, {"STARTUP_TIMEOUT=0s"},
		{"PROBE_TIMEOUT=11m"}, {"RETRY_DELAY=-1s"}, {"IMDS_TIMEOUT=no"},
		{"UNKNOWN_POLICY=reallocate"}, {"GPU=0,1"}, {"GPU=-x"},
		{"UPLOAD_BEARER_TOKEN=secret\nvalue"}, {"GPU_FREE_REASON=  "},
		{"GPU_TOTAL_REASON=line\nbreak"}, {"MULTIPLE_FAILURE_REASON=" + strings.Repeat("x", 1001)},
	} {
		t.Run(strings.Split(entries[0], "=")[0]+"/"+strings.Split(entries[0], "=")[1], func(t *testing.T) {
			env := make([]string, len(entries))
			for i, e := range entries {
				env[i] = Prefix + e
			}
			if _, err := Parse(env); err == nil {
				t.Fatal("accepted invalid configuration")
			} else if strings.Contains(err.Error(), "secret") {
				t.Fatal("leaked value")
			}
		})
	}
}

func TestValidNetworkConfigAndUnicodeReason(t *testing.T) {
	c := config(t, "DOWNLOAD_MIN_MBPS=1.5", "DOWNLOAD_URL=https://example.com/data?secret=hidden", "DOWNLOAD_SHA256="+strings.Repeat("AB", 32), "UPLOAD_MIN_MBPS=2", "UPLOAD_URL=https://example.com/upload", "GPU_FREE_REASON="+strings.Repeat("界", 1000), "ATTEMPTS=1", "FAIL_CONFIRMATIONS=1")
	if c.Download.SHA256 != strings.Repeat("ab", 32) || !c.Lookup("upload").Enabled {
		t.Fatal(c)
	}
}

func TestAddedChecksConfiguration(t *testing.T) {
	c := config(t)
	var names []string
	for _, ch := range c.Checks {
		names = append(names, ch.Name)
	}
	if got := strings.Join(names, " "); got != "gpu_total gpu_free gpu_power gpu_driver gpu_cuda gpu_utilization gpu_temperature latency download upload benchmark" {
		t.Fatal(got)
	}
	if c.Download.Bytes != 25_000_000 || c.Upload.Bytes != 25_000_000 || c.LatencySamples != 5 || c.Lookup("benchmark").Timeout != time.Minute || c.Lookup("gpu_total").Timeout != 0 {
		t.Fatal(c)
	}
	c = config(t, "GPU_DRIVER_MIN_VERSION=550.54.14", "GPU_CUDA_MIN_VERSION=12.8", "GPU_UTILIZATION_MAX_PERCENT=20", "GPU_TEMPERATURE_MAX_CELSIUS=85",
		"LATENCY_MAX_MS=150", "LATENCY_ADDRESS=storage.example.com:443", "LATENCY_SAMPLES=7", "LATENCY_REASON=too far",
		"BENCHMARK_MIN_SCORE=42.5", "BENCHMARK_COMMAND=/usr/local/bin/bench", "BENCHMARK_TIMEOUT=90s")
	for name, want := range map[string]Check{
		"gpu_driver":      {Threshold: 550054014, ThresholdText: "550.54.14", Version: true},
		"gpu_cuda":        {Threshold: 12008000, ThresholdText: "12.8", Version: true},
		"gpu_utilization": {Threshold: 20, AtMost: true},
		"gpu_temperature": {Threshold: 85, AtMost: true},
		"latency":         {Threshold: 150, AtMost: true},
		"benchmark":       {Threshold: 42.5, Timeout: 90 * time.Second},
	} {
		got := c.Lookup(name)
		if !got.Enabled || got.Threshold != want.Threshold || got.ThresholdText != want.ThresholdText || got.AtMost != want.AtMost || got.Version != want.Version || got.Timeout != want.Timeout {
			t.Fatalf("%s: %+v", name, got)
		}
	}
	if c.LatencyAddress != "storage.example.com:443" || c.LatencySamples != 7 || c.Lookup("latency").Reason != "too far" || c.BenchmarkCommand != "/usr/local/bin/bench" {
		t.Fatal(c)
	}
	if c.Lookup("gpu_total").Enabled || c.Lookup("download").Enabled || c.Lookup("missing").Enabled {
		t.Fatal("enabled a check without a threshold")
	}
	if c = config(t, "LATENCY_MAX_MS=150", "LATENCY_ENABLED=false", "LATENCY_ADDRESS=[2001:db8::1]:443"); c.Lookup("latency").Enabled {
		t.Fatal("explicit false must disable")
	}
}

func TestVersionEncodingOrdersLikeVersions(t *testing.T) {
	for _, pair := range [][2]string{{"12.8", "12.10"}, {"12.10", "13"}, {"535.183.01", "550.54.14"}, {"561.9", "561.19"}, {"12", "12.0.1"}} {
		low, ok1 := parseVersion(pair[0])
		high, ok2 := parseVersion(pair[1])
		if !ok1 || !ok2 || low >= high {
			t.Fatal(pair, low, high)
		}
	}
	if a, _ := parseVersion("12.8"); a != 12008000 {
		t.Fatal(a)
	}
	if a, _ := parseVersion("12"); a != 12000000 {
		t.Fatal(a)
	}
	for _, bad := range []string{"", ".", "12.", ".8", "1.2.3.4", "1000", "12.x", "N/A", "-1", "1 2", "１２"} {
		if _, ok := parseVersion(bad); ok {
			t.Fatal("accepted", bad)
		}
	}
}

func TestInvalidAddedConfiguration(t *testing.T) {
	for _, entries := range [][]string{
		{"GPU_CUDA_MIN_VERSION=12.x"}, {"GPU_DRIVER_MIN_VERSION=1.2.3.4"}, {"GPU_DRIVER_MIN_VERSION=1000"},
		{"GPU_CUDA_MIN_VERSION=0"}, {"GPU_CUDA_ENABLED=true"}, {"GPU_UTILIZATION_MAX_PERCENT=-5"},
		{"GPU_TEMPERATURE_MAX_CELSIUS=NaN"}, {"GPU_TEMPERATURE_MAX_CELSIUS=0"},
		{"LATENCY_MAX_MS=50"}, {"LATENCY_ADDRESS=secret-host"}, {"LATENCY_ADDRESS=secret-host:0"},
		{"LATENCY_ADDRESS=secret-host:99999"}, {"LATENCY_ADDRESS=:443"}, {"LATENCY_ADDRESS=secret host:443"},
		{"LATENCY_ADDRESS=https://secret-host:443/x"}, {"LATENCY_SAMPLES=0"}, {"LATENCY_SAMPLES=21"},
		{"BENCHMARK_MIN_SCORE=10"}, {"BENCHMARK_ENABLED=true", "BENCHMARK_COMMAND=/secret/bench"},
		{"BENCHMARK_TIMEOUT=11m"}, {"BENCHMARK_TIMEOUT=0s"}, {"BENCHMARK_MIN_SCORE=secret"},
		{"GPU_DRIVER_MIN_MIB=1"}, {"LATENCY_MIN_MS=1"},
	} {
		t.Run(strings.Join(entries, ","), func(t *testing.T) {
			env := make([]string, len(entries))
			for i, e := range entries {
				env[i] = Prefix + e
			}
			if _, err := Parse(env); err == nil {
				t.Fatal("accepted invalid configuration")
			} else if strings.Contains(err.Error(), "secret") {
				t.Fatal("leaked value")
			}
		})
	}
}
