package preflight

import (
	"context"
	"math"
	"time"
)

type Event struct {
	Event           string   `json:"event"`
	Check           string   `json:"check,omitempty"`
	Result          string   `json:"result,omitempty"`
	Measurement     *float64 `json:"measurement"`
	Threshold       *float64 `json:"threshold"`
	Units           string   `json:"units,omitempty"`
	ObservedAmount  *float64 `json:"observed_amount,omitempty"`
	ReferenceAmount *float64 `json:"reference_amount,omitempty"`
	SourceUnits     string   `json:"source_units,omitempty"`
	ReferenceField  string   `json:"reference_field,omitempty"`
	// Comparison is "at_most" for maximum checks and absent for minimum checks.
	Comparison string `json:"comparison,omitempty"`
	// Version checks also carry the readable versions behind the encoded numbers.
	MeasurementText  string      `json:"measurement_text,omitempty"`
	ThresholdText    string      `json:"threshold_text,omitempty"`
	Attempt          int         `json:"attempt,omitempty"`
	Detail           string      `json:"detail,omitempty"`
	Action           string      `json:"action,omitempty"`
	Reason           string      `json:"reason,omitempty"`
	ReasonCode       string      `json:"reason_code,omitempty"`
	FailedChecks     []string    `json:"failed_checks,omitempty"`
	IMDS             *IMDSResult `json:"imds,omitempty"`
	MachineID        string      `json:"machine_id,omitempty"`
	InstanceID       string      `json:"instance_id,omitempty"`
	ContainerGroupID string      `json:"container_group_id,omitempty"`
}

type Emit func(Event)

type Decision struct {
	Launch   bool
	ExitCode int
}

func evaluate(ctx context.Context, c Config, check Check, probe Probe, emit Emit) string {
	event := Event{Event: "check_result", Check: check.Name, Result: "skipped", Threshold: &check.Threshold, ThresholdText: check.ThresholdText, Units: check.Unit}
	event.SourceUnits, event.ReferenceField = check.SourceUnit, check.ReferenceField
	if check.Threshold == 0 {
		event.Threshold = nil
	}
	if check.AtMost {
		event.Comparison = "at_most"
	}
	timeout := c.ProbeTimeout
	if check.Timeout > 0 {
		timeout = check.Timeout
	}
	if !check.Enabled {
		emit(event)
		return "skipped"
	}
	streak := 0
	event.Result = "unknown"
	for attempt := 1; attempt <= c.Attempts; attempt++ {
		if ctx.Err() != nil {
			event.Detail = "startup_deadline_or_cancelled"
			break
		}
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		m := probe(probeCtx, check)
		if probeCtx.Err() != nil {
			m = unknown("probe_timeout_or_cancelled")
		}
		cancel()
		if m.Value != nil && (math.IsNaN(*m.Value) || math.IsInf(*m.Value, 0) || *m.Value < 0) {
			m = unknown("invalid_measurement")
		}
		event.Attempt, event.Measurement, event.MeasurementText, event.Detail = attempt, m.Value, m.Text, m.Detail
		event.ObservedAmount, event.ReferenceAmount = m.ObservedAmount, m.ReferenceAmount
		attemptEvent := event
		attemptEvent.Event = "measurement"
		switch {
		case m.Value == nil:
			streak = 0
			attemptEvent.Result = "unknown"
		case check.AtMost && *m.Value > check.Threshold, !check.AtMost && *m.Value < check.Threshold:
			streak++
			attemptEvent.Result = "fail"
		default: // equality passes in both directions
			attemptEvent.Result = "pass"
		}
		emit(attemptEvent)
		if attemptEvent.Result == "pass" {
			event.Result = "pass"
			break
		}
		if streak >= c.Confirmations {
			event.Result = "fail"
			break
		}
		if attempt < c.Attempts {
			timer := time.NewTimer(c.RetryDelay)
			select {
			case <-ctx.Done():
			case <-timer.C:
			}
			timer.Stop()
		}
	}
	if event.Result == "unknown" && event.Detail == "" {
		event.Detail = "insufficient_failure_confirmation"
	}
	if event.Result == "fail" {
		event.Reason = check.Reason
		event.ReasonCode = reasonCode(check)
	}
	emit(event)
	return event.Result
}

// Run's context bounds all probes, retry waits and the single IMDS call together.
func Run(parent context.Context, c Config, probe Probe, reallocate Reallocate, emit Emit) Decision {
	ctx, cancel := context.WithTimeout(parent, c.StartupTimeout)
	defer cancel()
	var failures []Check
	unknowns := false
	for _, check := range c.Checks {
		switch evaluate(ctx, c, check, probe, emit) {
		case "fail":
			failures = append(failures, check)
		case "unknown":
			unknowns = true
		}
	}
	if parent.Err() != nil {
		emit(Event{Event: "decision", Action: "block", Detail: "interrupted"})
		return Decision{ExitCode: 24}
	}
	if len(failures) > 0 {
		reason, code, checks := reallocationReason(failures, c.MultipleReason)
		if c.DryRun {
			emit(Event{Event: "decision", Action: "would_reallocate", Reason: reason, ReasonCode: code, FailedChecks: checks, IMDS: &IMDSResult{Outcome: "dry_run"}})
		} else {
			emit(Event{Event: "decision", Action: "request_reallocation", Reason: reason, ReasonCode: code, FailedChecks: checks})
			imdsCtx, done := context.WithTimeout(ctx, c.IMDSTimeout)
			result := IMDSResult{Outcome: "not_sent"}
			if imdsCtx.Err() == nil {
				result = reallocate(imdsCtx, reason)
			}
			done()
			emit(Event{Event: "imds_result", Action: "block", Reason: reason, ReasonCode: code, FailedChecks: checks, IMDS: &result})
			code := 22
			switch result.Outcome {
			case "accepted":
				code = 20
			case "rejected":
				code = 21
			}
			return Decision{ExitCode: code}
		}
	}
	if unknowns && c.UnknownPolicy == "block" {
		emit(Event{Event: "decision", Action: "block", Detail: "unknown_policy"})
		return Decision{ExitCode: 23}
	}
	emit(Event{Event: "decision", Action: "launch"})
	return Decision{Launch: true}
}
