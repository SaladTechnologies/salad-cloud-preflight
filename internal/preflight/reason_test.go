package preflight

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReasonContractEveryCheck(t *testing.T) {
	c := config(t)
	want := []string{"gpu_total_below_minimum", "gpu_free_below_minimum", "gpu_power_below_minimum", "gpu_driver_below_minimum", "gpu_cuda_below_minimum", "gpu_utilization_above_maximum", "gpu_temperature_above_maximum", "latency_above_maximum", "download_below_minimum", "upload_below_minimum", "benchmark_below_minimum"}
	for i, check := range c.Checks {
		check.Reason = "custom \"message\" 界"
		reason, code, checks := reallocationReason([]Check{check}, "unused")
		if code != want[i] || len(checks) != 1 || checks[0] != check.Name || reason != "salad-preflight/v1 code="+want[i]+" checks="+check.Name+": "+check.Reason {
			t.Fatal(reason, code, checks)
		}
	}
}

func TestReasonLengthAndMultipleFailures(t *testing.T) {
	c := config(t)
	c.Checks[0].Reason = strings.Repeat("界", 1000)
	for _, checks := range [][]Check{c.Checks[:1], c.Checks} {
		reason, _, names := reallocationReason(checks, strings.Repeat("界", 1000))
		if !utf8.ValidString(reason) || utf8.RuneCountInString(reason) != 1000 || !strings.HasSuffix(reason, "…") || len(names) != len(checks) {
			t.Fatal("invalid bounded reason", reason)
		}
		for _, check := range checks {
			if !strings.Contains(reason, check.Name) {
				t.Fatal("lost failure", check.Name)
			}
		}
	}
}

func TestReasonSentToIMDSMatchesDecision(t *testing.T) {
	var received, logged string
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		var body struct {
			Reason string `json:"reason"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		received = body.Reason
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	c := config(t, "GPU_FREE_MIN_MIB=10", "DRY_RUN=false", "RETRY_DELAY=0s")
	d := Run(context.Background(), c, func(context.Context, Check) Measurement { return measured(1) }, func(ctx context.Context, reason string) IMDSResult {
		return postReallocation(ctx, client, "http://imds.invalid/v1/reallocate", reason)
	}, func(e Event) {
		if e.Action == "request_reallocation" {
			logged = e.Reason
		}
	})
	if d.Launch || d.ExitCode != 20 || calls != 1 || received != logged || !strings.HasPrefix(received, "salad-preflight/v1 code=gpu_free_below_minimum checks=gpu_free: ") {
		t.Fatal(d, calls, received, logged)
	}
}

func TestEveryCheckCanEnforceOnlyConfirmedFailure(t *testing.T) {
	base := config(t)
	for _, check := range base.Checks {
		for _, unknownResult := range []bool{false, true} {
			t.Run(check.Name+"/unknown="+fmt.Sprint(unknownResult), func(t *testing.T) {
				c := config(t, "DRY_RUN=false", "RETRY_DELAY=0s")
				check.Enabled, check.Threshold = true, 1
				check.Reason = "customer message"
				c.Checks = []Check{check}
				probes, posts := 0, 0
				d := Run(context.Background(), c, func(context.Context, Check) Measurement {
					probes++
					if unknownResult {
						return unknown("unavailable")
					}
					if check.AtMost {
						return measured(2)
					}
					return measured(0)
				}, func(_ context.Context, reason string) IMDSResult {
					posts++
					if probes != c.Confirmations || !strings.HasSuffix(reason, " checks="+check.Name+": customer message") {
						t.Fatal(probes, reason)
					}
					return IMDSResult{Outcome: "accepted", Status: 204}
				}, func(Event) {})
				if unknownResult {
					if !d.Launch || posts != 0 || probes != c.Attempts {
						t.Fatal(d, posts, probes)
					}
				} else if d.Launch || d.ExitCode != 20 || posts != 1 {
					t.Fatal(d, posts)
				}
			})
		}
	}
}
