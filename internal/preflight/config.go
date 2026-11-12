package preflight

import (
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const Prefix = "SALAD_PREFLIGHT_"

// Check is one requirement. Threshold is a minimum unless AtMost is set.
type Check struct {
	Name, Unit, Reason, Field  string
	ReferenceField, SourceUnit string // percentage mode: Field / ReferenceField
	Enabled                    bool
	Threshold                  float64
	ThresholdText              string        // readable form of a version threshold
	AtMost                     bool          // the measurement must not exceed Threshold
	Version                    bool          // dotted version rather than a plain number
	ZeroValid                  bool          // a GPU reading of zero is a real value
	Ceiling                    float64       // GPU readings above this are implausible; 0 means no bound
	Timeout                    time.Duration // per-attempt deadline; 0 means PROBE_TIMEOUT
}

type Network struct {
	URL, Token, SHA256 string
	Bytes              int64
}

type Config struct {
	DryRun                                                bool
	UnknownPolicy, GPU, SMI, MultipleReason               string
	LatencyAddress, BenchmarkCommand                      string
	StartupTimeout, ProbeTimeout, IMDSTimeout, RetryDelay time.Duration
	Attempts, Confirmations, LatencySamples               int
	Download, Upload                                      Network
	Checks                                                []Check
}

// Lookup returns the named check, or a disabled zero value when there is none.
func (c Config) Lookup(name string) Check {
	for _, check := range c.Checks {
		if check.Name == name {
			return check
		}
	}
	return Check{}
}

// parseVersion encodes one to three dotted numbers, each 0–999, as
// major×1,000,000 + minor×1,000 + patch so versions compare as plain numbers.
func parseVersion(s string) (float64, bool) {
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return 0, false
	}
	v := 0.0
	for i := 0; i < 3; i++ {
		n := 0
		if i < len(parts) {
			if parts[i] == "" || len(parts[i]) > 3 {
				return 0, false
			}
			for _, r := range parts[i] {
				if r < '0' || r > '9' {
					return 0, false
				}
				n = n*10 + int(r-'0')
			}
		}
		v = v*1000 + float64(n)
	}
	return v, true
}

// Parse validates the entire configuration before any side effects. Error messages
// deliberately name keys, never their potentially sensitive values.
func Parse(env []string) (Config, error) {
	c := Config{}
	values := map[string]string{}
	for _, entry := range env {
		k, v, ok := strings.Cut(entry, "=")
		if ok && strings.HasPrefix(k, Prefix) {
			values[strings.TrimPrefix(k, Prefix)] = v
		}
	}
	used := map[string]bool{}
	var first error
	bad := func(k, rule string) {
		if first == nil {
			first = fmt.Errorf("%s%s %s", Prefix, k, rule)
		}
	}
	get := func(k, fallback string) string {
		used[k] = true
		if v, ok := values[k]; ok && v != "" {
			return v
		}
		return fallback
	}
	boolean := func(k string, fallback bool) bool {
		v := get(k, strconv.FormatBool(fallback))
		if v != "true" && v != "false" {
			bad(k, "must be true or false")
		}
		return v == "true"
	}
	integer := func(k string, fallback, max int64) int64 {
		v, err := strconv.ParseInt(get(k, strconv.FormatInt(fallback, 10)), 10, 64)
		if err != nil || v < 1 || v > max {
			bad(k, fmt.Sprintf("must be an integer from 1 to %d", max))
		}
		return v
	}
	duration := func(k, fallback string, allowZero bool) time.Duration {
		v, err := time.ParseDuration(get(k, fallback))
		if err != nil || v < 0 || (!allowZero && v == 0) || v > 10*time.Minute {
			bad(k, "must be a duration within 10m (positive unless retry delay)")
		}
		return v
	}
	reason := func(k, fallback string) string {
		v := get(k, fallback)
		if !utf8.ValidString(v) || strings.TrimSpace(v) == "" || utf8.RuneCountInString(v) > 1000 || strings.IndexFunc(v, unicode.IsControl) >= 0 {
			bad(k, "must contain 1–1000 characters without control characters")
		}
		return v
	}
	c.DryRun = boolean("DRY_RUN", true)
	c.UnknownPolicy = get("UNKNOWN_POLICY", "continue")
	if c.UnknownPolicy != "continue" && c.UnknownPolicy != "block" {
		bad("UNKNOWN_POLICY", "must be continue or block")
	}
	c.GPU = get("GPU", "0")
	if strings.ContainsAny(c.GPU, ", \t\n\r") || strings.HasPrefix(c.GPU, "-") {
		bad("GPU", "must select one GPU index, UUID or PCI bus ID")
	}
	c.SMI = get("NVIDIA_SMI", "nvidia-smi")
	c.StartupTimeout = duration("STARTUP_TIMEOUT", "120s", false)
	c.ProbeTimeout = duration("PROBE_TIMEOUT", "15s", false)
	c.IMDSTimeout = duration("IMDS_TIMEOUT", "5s", false)
	c.RetryDelay = duration("RETRY_DELAY", "1s", true)
	c.Attempts = int(integer("ATTEMPTS", 3, 10))
	c.Confirmations = int(integer("FAIL_CONFIRMATIONS", 2, 10))
	if c.Confirmations > c.Attempts {
		bad("FAIL_CONFIRMATIONS", "must be no greater than ATTEMPTS")
	}
	c.MultipleReason = reason("MULTIPLE_FAILURE_REASON", "Multiple startup requirements not met")
	powerReference := get("GPU_POWER_REFERENCE", "default")
	if powerReference != "default" && powerReference != "max" {
		bad("GPU_POWER_REFERENCE", "must be default or max")
	}
	benchmarkTimeout := duration("BENCHMARK_TIMEOUT", "60s", false)
	// Checks run in this order: quick GPU queries first, the benchmark last.
	for _, spec := range []struct {
		key, threshold, name, unit, field, why string
		atMost, version, zeroValid             bool
		ceiling                                float64
	}{
		{key: "GPU_TOTAL", threshold: "MIN_MIB", name: "gpu_total", unit: "MiB", field: "memory.total", why: "GPU total VRAM below minimum"},
		{key: "GPU_FREE", threshold: "MIN_MIB", name: "gpu_free", unit: "MiB", field: "memory.free", why: "GPU free VRAM below minimum", zeroValid: true},
		{key: "GPU_POWER", threshold: "MIN_WATTS", name: "gpu_power", unit: "watts", field: "power.limit", why: "GPU power limit below minimum"},
		{key: "GPU_DRIVER", threshold: "MIN_VERSION", name: "gpu_driver", unit: "version", field: "driver_version", why: "GPU driver version below minimum", version: true},
		{key: "GPU_CUDA", threshold: "MIN_VERSION", name: "gpu_cuda", unit: "version", why: "CUDA version below minimum", version: true},
		{key: "GPU_UTILIZATION", threshold: "MAX_PERCENT", name: "gpu_utilization", unit: "percent", field: "utilization.gpu", why: "GPU utilization above maximum", atMost: true, zeroValid: true, ceiling: 100},
		{key: "GPU_TEMPERATURE", threshold: "MAX_CELSIUS", name: "gpu_temperature", unit: "celsius", field: "temperature.gpu", why: "GPU temperature above maximum", atMost: true, ceiling: 150},
		{key: "LATENCY", threshold: "MAX_MS", name: "latency", unit: "ms", why: "Network latency above maximum", atMost: true},
		{key: "DOWNLOAD", threshold: "MIN_MBPS", name: "download", unit: "Mbps", why: "Download bandwidth below minimum"},
		{key: "UPLOAD", threshold: "MIN_MBPS", name: "upload", unit: "Mbps", why: "Upload bandwidth below minimum"},
		{key: "BENCHMARK", threshold: "MIN_SCORE", name: "benchmark", unit: "score", why: "Benchmark score below minimum"},
	} {
		k := spec.key + "_" + spec.threshold
		raw := get(k, "")
		reference := ""
		if spec.name == "gpu_free" {
			percent := get("GPU_FREE_MIN_PERCENT", "")
			if percent != "" {
				if raw != "" {
					bad("GPU_FREE_MIN_PERCENT", "cannot be combined with GPU_FREE_MIN_MIB")
				}
				k, raw, reference = "GPU_FREE_MIN_PERCENT", percent, "memory.total"
			}
		}
		if spec.name == "gpu_power" {
			percent := get("GPU_POWER_MIN_PERCENT", "")
			if percent != "" {
				if raw != "" {
					bad("GPU_POWER_MIN_PERCENT", "cannot be combined with GPU_POWER_MIN_WATTS")
				}
				k, raw, reference = "GPU_POWER_MIN_PERCENT", percent, "power."+powerReference+"_limit"
			}
		}
		v, text := float64(0), ""
		if raw != "" && spec.version {
			var ok bool
			if v, ok = parseVersion(raw); !ok || v == 0 {
				bad(k, "must be a version of one to three numbers from 0 to 999, such as 12.8")
			} else {
				text = raw
			}
		} else if raw != "" {
			var err error
			v, err = strconv.ParseFloat(raw, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
				bad(k, "must be a finite positive number")
			}
		}
		if reference != "" && v > 100 {
			bad(k, "must be greater than zero and at most 100")
		}
		enabled := boolean(spec.key+"_ENABLED", raw != "")
		if enabled && raw == "" {
			bad(k, "is required when the check is enabled")
		}
		check := Check{Name: spec.name, Unit: spec.unit, Field: spec.field, Reason: reason(spec.key+"_REASON", spec.why), Threshold: v, ThresholdText: text, Enabled: enabled, AtMost: spec.atMost, Version: spec.version, ZeroValid: spec.zeroValid, Ceiling: spec.ceiling}
		if reference != "" {
			check.ReferenceField, check.SourceUnit, check.Unit = reference, spec.unit, "percent"
		}
		if spec.name == "benchmark" {
			check.Timeout = benchmarkTimeout
		}
		c.Checks = append(c.Checks, check)
	}
	for _, spec := range []struct {
		key     string
		n       *Network
		enabled bool
	}{{"DOWNLOAD", &c.Download, c.Lookup("download").Enabled}, {"UPLOAD", &c.Upload, c.Lookup("upload").Enabled}} {
		n := spec.n
		n.URL = get(spec.key+"_URL", "")
		if n.URL != "" {
			u, err := url.Parse(n.URL)
			if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
				bad(spec.key+"_URL", "must be an HTTP(S) URL without userinfo or fragment")
			}
		} else if spec.enabled {
			bad(spec.key+"_URL", "is required when enabled")
		}
		n.Bytes = integer(spec.key+"_BYTES", 25_000_000, 1024*1024*1024)
		n.Token = get(spec.key+"_BEARER_TOKEN", "")
		if strings.IndexFunc(n.Token, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
			bad(spec.key+"_BEARER_TOKEN", "must be a single printable ASCII token")
		}
	}
	c.Download.SHA256 = strings.ToLower(get("DOWNLOAD_SHA256", ""))
	if c.Download.SHA256 != "" || c.Lookup("download").Enabled {
		b, err := hex.DecodeString(c.Download.SHA256)
		if err != nil || len(b) != 32 {
			bad("DOWNLOAD_SHA256", "must be the 64 hex digit digest of the download object")
		}
	}
	c.LatencyAddress = get("LATENCY_ADDRESS", "")
	if c.LatencyAddress != "" {
		host, port, err := net.SplitHostPort(c.LatencyAddress)
		n, portErr := strconv.Atoi(port)
		if err != nil || host == "" || portErr != nil || n < 1 || n > 65535 || strings.IndexFunc(c.LatencyAddress, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
			bad("LATENCY_ADDRESS", "must be host:port with a port from 1 to 65535")
		}
	} else if c.Lookup("latency").Enabled {
		bad("LATENCY_ADDRESS", "is required when enabled")
	}
	c.LatencySamples = int(integer("LATENCY_SAMPLES", 5, 20))
	c.BenchmarkCommand = get("BENCHMARK_COMMAND", "")
	if c.BenchmarkCommand == "" && c.Lookup("benchmark").Enabled {
		bad("BENCHMARK_COMMAND", "is required when enabled")
	}
	for k := range values {
		if !used[k] {
			bad(k, "is not a recognized configuration key")
		}
	}
	return c, first
}
