package preflight

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMeasurementConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []Measurement
		want   string
		calls  int
	}{
		{"equal", []Measurement{measured(10)}, "pass", 1},
		{"above", []Measurement{measured(11)}, "pass", 1},
		{"confirmed_low", []Measurement{measured(9), measured(8)}, "fail", 2},
		{"transient_low", []Measurement{measured(9), measured(10)}, "pass", 2},
		{"unknown_recovery", []Measurement{unknown("missing"), measured(9), measured(8)}, "fail", 3},
		{"broken_streak", []Measurement{measured(9), unknown("missing"), measured(9)}, "unknown", 3},
		{"missing", []Measurement{unknown("missing"), unknown("missing"), unknown("missing")}, "unknown", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config(t, "GPU_FREE_MIN_MIB=10", "RETRY_DELAY=0s")
			calls := 0
			var events []Event
			got := evaluate(context.Background(), c, c.Checks[1], func(context.Context, Check) Measurement { m := tc.values[calls]; calls++; return m }, func(e Event) { events = append(events, e) })
			if got != tc.want || calls != tc.calls {
				t.Fatalf("%s / %d", got, calls)
			}
			last := events[len(events)-1]
			if last.Event != "check_result" || last.Threshold == nil || *last.Threshold != 10 || last.Units != "MiB" {
				t.Fatal(last)
			}
		})
	}
}

func TestDecisions(t *testing.T) {
	for _, tc := range []struct {
		name        string
		env         []string
		m           Measurement
		outcome     string
		launch      bool
		code, calls int
		reason      string
	}{
		{"default_disabled", nil, measured(1), "", true, 0, 0, ""},
		{"dry_run", []string{"GPU_FREE_MIN_MIB=10"}, measured(1), "", true, 0, 0, "GPU free VRAM below minimum"},
		{"unknown_continue", []string{"GPU_FREE_MIN_MIB=10", "DRY_RUN=false"}, unknown("missing"), "", true, 0, 0, ""},
		{"unknown_block", []string{"GPU_FREE_MIN_MIB=10", "UNKNOWN_POLICY=block"}, unknown("missing"), "", false, 23, 0, ""},
		{"accepted_custom", []string{"GPU_FREE_MIN_MIB=10", "DRY_RUN=false", "GPU_FREE_REASON=customer reason"}, measured(1), "accepted", false, 20, 1, "customer reason"},
		{"rejected", []string{"GPU_FREE_MIN_MIB=10", "DRY_RUN=false"}, measured(1), "rejected", false, 21, 1, "GPU free VRAM below minimum"},
		{"uncertain", []string{"GPU_FREE_MIN_MIB=10", "DRY_RUN=false"}, measured(1), "uncertain", false, 22, 1, "GPU free VRAM below minimum"},
		{"multiple", []string{"GPU_FREE_MIN_MIB=10", "GPU_TOTAL_MIN_MIB=20", "DRY_RUN=false", "MULTIPLE_FAILURE_REASON=combined reason"}, measured(1), "accepted", false, 20, 1, "combined reason"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config(t, append(tc.env, "RETRY_DELAY=0s")...)
			calls, probes := 0, 0
			var events []Event
			d := Run(context.Background(), c, func(context.Context, Check) Measurement { probes++; return tc.m }, func(ctx context.Context, reason string) IMDSResult {
				calls++
				prefix := "salad-preflight/v1 code=gpu_free_below_minimum checks=gpu_free: "
				if tc.name == "multiple" {
					prefix = "salad-preflight/v1 code=multiple_checks_failed checks=gpu_total,gpu_free: "
				}
				if reason != prefix+tc.reason {
					t.Fatal(reason)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("missing IMDS deadline")
				}
				return IMDSResult{Outcome: tc.outcome}
			}, func(e Event) { events = append(events, e) })
			if d.Launch != tc.launch || d.ExitCode != tc.code || calls != tc.calls {
				t.Fatalf("%+v, calls=%d", d, calls)
			}
			if tc.name == "default_disabled" && probes != 0 {
				t.Fatal("probed disabled check")
			}
			if tc.reason != "" {
				found := false
				for _, e := range events {
					if e.Reason == tc.reason || strings.HasSuffix(e.Reason, ": "+tc.reason) {
						found = true
					}
				}
				if !found {
					t.Fatal("missing reason")
				}
			}
		})
	}
}

func TestReasonDoesNotAffectLogic(t *testing.T) {
	for _, reason := range []string{"pass", "unknown", "launch", "reallocate", "任意の理由"} {
		c := config(t, "GPU_POWER_MIN_WATTS=100", "GPU_POWER_REASON="+reason, "RETRY_DELAY=0s")
		got := evaluate(context.Background(), c, c.Checks[2], func(context.Context, Check) Measurement { return measured(50) }, func(Event) {})
		if got != "fail" {
			t.Fatal(got)
		}
	}
}

func TestGlobalAndProbeDeadlines(t *testing.T) {
	c := config(t, "GPU_FREE_MIN_MIB=10", "GPU_TOTAL_MIN_MIB=10", "STARTUP_TIMEOUT=30ms", "PROBE_TIMEOUT=10ms", "RETRY_DELAY=1s", "UNKNOWN_POLICY=block")
	start := time.Now()
	d := Run(context.Background(), c, func(ctx context.Context, _ Check) Measurement { <-ctx.Done(); return measured(0) }, func(context.Context, string) IMDSResult { t.Fatal("unexpected IMDS"); return IMDSResult{} }, func(Event) {})
	if d.Launch || d.ExitCode != 23 || time.Since(start) > time.Second {
		t.Fatal(d)
	}
}

func TestExpiredBudgetNeverPosts(t *testing.T) {
	c := config(t, "GPU_TOTAL_MIN_MIB=10", "GPU_FREE_MIN_MIB=10", "DRY_RUN=false", "STARTUP_TIMEOUT=20ms", "RETRY_DELAY=0s")
	var outcome string
	d := Run(context.Background(), c, func(ctx context.Context, ch Check) Measurement {
		if ch.Name == "gpu_total" {
			return measured(1)
		}
		<-ctx.Done()
		return unknown("timeout")
	}, func(context.Context, string) IMDSResult { t.Fatal("posted after deadline"); return IMDSResult{} }, func(e Event) {
		if e.IMDS != nil {
			outcome = e.IMDS.Outcome
		}
	})
	if d.Launch || d.ExitCode != 22 || outcome != "not_sent" {
		t.Fatal(d, outcome)
	}
}

func TestCancelledRunNeverLaunches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := Run(ctx, config(t), func(context.Context, Check) Measurement { t.Fatal("probe"); return Measurement{} }, func(context.Context, string) IMDSResult { t.Fatal("IMDS"); return IMDSResult{} }, func(Event) {})
	if d.Launch || d.ExitCode != 24 {
		t.Fatal(d)
	}
}

func TestLogsDoNotExposeEndpointSecrets(t *testing.T) {
	c := config(t, "UPLOAD_MIN_MBPS=10", "UPLOAD_URL=http://127.0.0.1:1/?password=secretvalue", "UPLOAD_BEARER_TOKEN=tokenvalue", "RETRY_DELAY=0s")
	var log strings.Builder
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("endpoint %s token %s failed", r.URL, r.Header.Get("Authorization"))
	})}
	Run(context.Background(), c, func(ctx context.Context, _ Check) Measurement { return networkProbe(ctx, c.Upload, true, client) }, func(context.Context, string) IMDSResult { t.Fatal("IMDS"); return IMDSResult{} }, func(e Event) { b, _ := json.Marshal(e); log.Write(b) })
	for _, secret := range []string{"secretvalue", "tokenvalue", "127.0.0.1", "password="} {
		if strings.Contains(log.String(), secret) {
			t.Fatal("secret leaked")
		}
	}
}

func TestMaximumConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []Measurement
		want   string
		calls  int
	}{
		{"equal", []Measurement{measured(80)}, "pass", 1},
		{"below", []Measurement{measured(45)}, "pass", 1},
		{"confirmed_high", []Measurement{measured(81), measured(95)}, "fail", 2},
		{"transient_high", []Measurement{measured(81), measured(80)}, "pass", 2},
		{"unknown_recovery", []Measurement{unknown("missing"), measured(81), measured(82)}, "fail", 3},
		{"broken_streak", []Measurement{measured(81), unknown("missing"), measured(81)}, "unknown", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config(t, "GPU_TEMPERATURE_MAX_CELSIUS=80", "RETRY_DELAY=0s")
			calls := 0
			var events []Event
			got := evaluate(context.Background(), c, c.Lookup("gpu_temperature"), func(context.Context, Check) Measurement { m := tc.values[calls]; calls++; return m }, func(e Event) { events = append(events, e) })
			if got != tc.want || calls != tc.calls {
				t.Fatalf("%s / %d", got, calls)
			}
			for _, e := range events {
				if e.Comparison != "at_most" || e.Threshold == nil || *e.Threshold != 80 || e.Units != "celsius" {
					t.Fatal(e)
				}
			}
			if last := events[len(events)-1]; last.Event != "check_result" || (tc.want == "fail") != (last.Reason == "GPU temperature above maximum") {
				t.Fatal(last)
			}
		})
	}
	c := config(t, "GPU_FREE_MIN_MIB=10", "RETRY_DELAY=0s")
	evaluate(context.Background(), c, c.Lookup("gpu_free"), func(context.Context, Check) Measurement { return measured(10) }, func(e Event) {
		if e.Comparison != "" {
			t.Fatal("minimum checks carry no comparison")
		}
	})
}

func TestVersionEventsCarryReadableText(t *testing.T) {
	c := config(t, "GPU_CUDA_MIN_VERSION=12.8", "RETRY_DELAY=0s")
	low := 12006000.0
	var log strings.Builder
	got := evaluate(context.Background(), c, c.Lookup("gpu_cuda"), func(context.Context, Check) Measurement { return Measurement{Value: &low, Text: "12.6"} }, func(e Event) { b, _ := json.Marshal(e); log.Write(b); log.WriteString("\n") })
	lines := strings.Split(strings.TrimSpace(log.String()), "\n")
	want := `{"event":"check_result","check":"gpu_cuda","result":"fail","measurement":12006000,"threshold":12008000,"units":"version","measurement_text":"12.6","threshold_text":"12.8","attempt":2,"reason":"CUDA version below minimum","reason_code":"gpu_cuda_below_minimum"}`
	if got != "fail" || len(lines) != 3 || lines[2] != want {
		t.Fatal(got, log.String())
	}
	high := 12010000.0
	if got := evaluate(context.Background(), c, c.Lookup("gpu_cuda"), func(context.Context, Check) Measurement { return Measurement{Value: &high, Text: "12.10"} }, func(Event) {}); got != "pass" {
		t.Fatal("12.10 is newer than 12.8")
	}
}

func TestBenchmarkHasItsOwnDeadline(t *testing.T) {
	c := config(t, "BENCHMARK_MIN_SCORE=1", "BENCHMARK_COMMAND=/bench", "BENCHMARK_TIMEOUT=30ms", "GPU_FREE_MIN_MIB=1", "PROBE_TIMEOUT=10m", "ATTEMPTS=1", "FAIL_CONFIRMATIONS=1")
	remaining := map[string]time.Duration{}
	probe := func(ctx context.Context, ch Check) Measurement {
		deadline, _ := ctx.Deadline()
		remaining[ch.Name] = time.Until(deadline)
		if ch.Name == "benchmark" {
			<-ctx.Done()
		}
		return measured(5)
	}
	if got := evaluate(context.Background(), c, c.Lookup("gpu_free"), probe, func(Event) {}); got != "pass" {
		t.Fatal(got)
	}
	var detail string
	got := evaluate(context.Background(), c, c.Lookup("benchmark"), probe, func(e Event) { detail = e.Detail })
	if got != "unknown" || detail != "probe_timeout_or_cancelled" || remaining["benchmark"] > 30*time.Millisecond || remaining["gpu_free"] < 9*time.Minute {
		t.Fatal(got, detail, remaining)
	}
}

func TestIdentityIsStampedOnEveryEvent(t *testing.T) {
	id := Identify([]string{
		"SALAD_MACHINE_ID=5b1a2c3d-0000-4000-8000-123456789abc", "SALAD_INSTANCE_ID=instance-1", "SALAD_CONTAINER_GROUP_ID=group_1",
		"SALAD_ORGANIZATION_NAME=not-logged", "HOME=/root",
	})
	if id != (Identity{"5b1a2c3d-0000-4000-8000-123456789abc", "instance-1", "group_1"}) {
		t.Fatal(id)
	}
	for _, bad := range []string{"SALAD_MACHINE_ID=", "SALAD_MACHINE_ID=two words", "SALAD_MACHINE_ID=line\nbreak", "SALAD_MACHINE_ID=\"quoted\"", "SALAD_MACHINE_ID=" + strings.Repeat("a", 129), "SALAD_MACHINE_ID"} {
		if got := Identify([]string{bad}); got != (Identity{}) {
			t.Fatal("kept", bad)
		}
	}
	c := config(t, "GPU_FREE_MIN_MIB=10", "RETRY_DELAY=0s")
	var log strings.Builder
	events := 0
	Run(context.Background(), c, func(context.Context, Check) Measurement { return measured(1) }, func(context.Context, string) IMDSResult { t.Fatal("IMDS"); return IMDSResult{} }, id.Attach(func(e Event) {
		events++
		if e.MachineID != id.MachineID || e.InstanceID != "instance-1" || e.ContainerGroupID != "group_1" {
			t.Fatal(e)
		}
		b, _ := json.Marshal(e)
		log.Write(b)
	}))
	if events < 12 || !strings.Contains(log.String(), `"machine_id":"5b1a2c3d-0000-4000-8000-123456789abc","instance_id":"instance-1","container_group_id":"group_1"`) || strings.Contains(log.String(), "not-logged") {
		t.Fatal(events, log.String())
	}
	var plain strings.Builder
	Run(context.Background(), c, func(context.Context, Check) Measurement { return measured(1) }, nil, Identity{}.Attach(func(e Event) { b, _ := json.Marshal(e); plain.Write(b) }))
	if strings.Contains(plain.String(), "machine_id") || strings.Contains(plain.String(), "instance_id") {
		t.Fatal("empty identity must add nothing")
	}
}
