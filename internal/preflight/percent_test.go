package preflight

import (
	"context"
	"math"
	"strings"
	"testing"
)

func TestPercentageConfiguration(t *testing.T) {
	c := config(t, "GPU_FREE_MIN_PERCENT=95", "GPU_POWER_MIN_PERCENT=90")
	for _, tc := range []struct {
		name, reference string
		threshold       float64
	}{
		{"gpu_free", "memory.total", 95}, {"gpu_power", "power.default_limit", 90},
	} {
		ch := c.Lookup(tc.name)
		if !ch.Enabled || ch.Threshold != tc.threshold || ch.Unit != "percent" || ch.ReferenceField != tc.reference || ch.SourceUnit == "" {
			t.Fatal(ch)
		}
	}
	if got := config(t, "GPU_POWER_MIN_PERCENT=90", "GPU_POWER_REFERENCE=max").Lookup("gpu_power"); got.ReferenceField != "power.max_limit" {
		t.Fatal(got)
	}
	if got := config(t, "GPU_FREE_MIN_PERCENT=95", "GPU_FREE_ENABLED=false").Lookup("gpu_free"); got.Enabled {
		t.Fatal(got)
	}
	for _, entries := range [][]string{
		{"GPU_FREE_MIN_PERCENT=0"}, {"GPU_FREE_MIN_PERCENT=100.1"}, {"GPU_FREE_MIN_PERCENT=NaN"},
		{"GPU_FREE_MIN_PERCENT=-1"}, {"GPU_FREE_MIN_PERCENT=Inf", "GPU_FREE_ENABLED=false"},
		{"GPU_POWER_MIN_PERCENT=0"}, {"GPU_POWER_MIN_PERCENT=101"}, {"GPU_POWER_MIN_PERCENT=NaN"},
		{"GPU_POWER_MIN_PERCENT=Inf"}, {"GPU_POWER_REFERENCE=draw"},
		{"GPU_FREE_MIN_PERCENT=95", "GPU_FREE_MIN_MIB=100"},
		{"GPU_POWER_MIN_PERCENT=90", "GPU_POWER_MIN_WATTS=100"},
	} {
		env := make([]string, len(entries))
		for i, entry := range entries {
			env[i] = Prefix + entry
		}
		if _, err := Parse(env); err == nil {
			t.Fatal("accepted", entries)
		}
	}
}

func TestPercentageProbePairedReadings(t *testing.T) {
	c := config(t, "GPU=GPU-selected", "GPU_FREE_MIN_PERCENT=95", "GPU_POWER_MIN_PERCENT=90")
	for _, tc := range []struct {
		name, text string
		want       float64
		valid      bool
	}{
		{"gpu_free", "11717, 12288\n", 100 * 11717.0 / 12288, true},
		{"gpu_free", "19000, 20000", 95, true},
		{"gpu_free", "0, 12288", 0, true},
		{"gpu_free", "12000, 0", 0, false},
		{"gpu_free", "12289, 12288", 0, false},
		{"gpu_free", "N/A, 12288", 0, false},
		{"gpu_free", "11717, NaN", 0, false},
		{"gpu_free", "11717, Inf", 0, false},
		{"gpu_free", "11717, 12288\n11717, 12288", 0, false},
		{"gpu_free", "11717, 12288, 1", 0, false},
		{"gpu_power", "153, 170", 90, true},
		{"gpu_power", "136, 170", 80, true},
		{"gpu_power", "187, 170", 110, true},
		{"gpu_power", "0, 170", 0, false},
		{"gpu_power", "170, 0", 0, false},
		{"gpu_power", "170, [N/A]", 0, false},
		{"gpu_power", "-1, 170", 0, false},
	} {
		ch := c.Lookup(tc.name)
		m := GPUProbe(context.Background(), c, ch, func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "nvidia-smi" || strings.Join(args, " ") != "--id=GPU-selected --query-gpu="+ch.Field+","+ch.ReferenceField+" --format=csv,noheader,nounits" {
				t.Fatal(name, args)
			}
			return []byte(tc.text), nil
		})
		if (m.Value != nil) != tc.valid {
			t.Fatal(tc, m)
		}
		if tc.valid && (math.Abs(*m.Value-tc.want) > 1e-9 || m.ObservedAmount == nil || m.ReferenceAmount == nil) {
			t.Fatal(tc, m)
		}
		if !tc.valid && (m.Detail != "gpu_telemetry_unavailable" || m.ObservedAmount != nil || m.ReferenceAmount != nil) {
			t.Fatal(tc, m)
		}
	}
}

func TestPercentageDecisionsAndEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, key, reading string
		want               string
		code               int
		posts              int
	}{
		{"gpu_free", "GPU_FREE_MIN_PERCENT=95", "19000, 20000", "pass", 0, 0},
		{"gpu_free", "GPU_FREE_MIN_PERCENT=95", "18999, 20000", "fail", 20, 1},
		{"gpu_power", "GPU_POWER_MIN_PERCENT=90", "153, 170", "pass", 0, 0},
		{"gpu_power", "GPU_POWER_MIN_PERCENT=90", "152.9, 170", "fail", 20, 1},
		{"gpu_power", "GPU_POWER_MIN_PERCENT=90", "170, N/A", "unknown", 0, 0},
	} {
		c := config(t, tc.key, "DRY_RUN=false", "RETRY_DELAY=0s")
		posts, probes := 0, 0
		var event Event
		d := Run(context.Background(), c, func(ctx context.Context, ch Check) Measurement {
			probes++
			return GPUProbe(ctx, c, ch, func(context.Context, string, ...string) ([]byte, error) { return []byte(tc.reading), nil })
		}, func(_ context.Context, reason string) IMDSResult {
			posts++
			if probes != 2 || !strings.Contains(reason, "code="+tc.name+"_below_minimum") {
				t.Fatal(probes, reason)
			}
			return IMDSResult{Outcome: "accepted", Status: 204}
		}, func(e Event) {
			if e.Event == "check_result" && e.Check == tc.name {
				event = e
			}
		})
		if event.Result != tc.want || d.ExitCode != tc.code || posts != tc.posts || d.Launch != (tc.posts == 0) {
			t.Fatal(tc, d, event, posts)
		}
		if event.Units != "percent" || event.SourceUnits == "" || event.ReferenceField == "" {
			t.Fatal(event)
		}
		if tc.want != "unknown" && (event.ObservedAmount == nil || event.ReferenceAmount == nil) {
			t.Fatal(event)
		}
	}
}
