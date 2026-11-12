package preflight

import "strings"

// reasonCode is part of the v1 diagnostic contract, independent of custom text.
func reasonCode(check Check) string {
	if check.AtMost {
		return check.Name + "_above_maximum"
	}
	return check.Name + "_below_minimum"
}

// reallocationReason preserves the stable envelope even when custom text fills
// the IMDS limit. Only the human-readable suffix may be truncated, by rune.
func reallocationReason(failures []Check, multipleText string) (reason, code string, checks []string) {
	for _, check := range failures {
		checks = append(checks, check.Name)
	}
	code, message := reasonCode(failures[0]), failures[0].Reason
	if len(failures) > 1 {
		code, message = "multiple_checks_failed", multipleText
	}
	header := "salad-preflight/v1 code=" + code + " checks=" + strings.Join(checks, ",") + ": "
	text := []rune(message)
	budget := 1000 - len([]rune(header))
	if len(text) > budget {
		text = append(text[:budget-1], '…')
	}
	return header + string(text), code, checks
}
