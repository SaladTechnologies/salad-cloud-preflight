package preflight

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

const imdsURL = "http://169.254.169.254/v1/reallocate"

type IMDSResult struct {
	Outcome string `json:"outcome"`
	Status  int    `json:"http_status,omitempty"`
}

type Reallocate func(context.Context, string) IMDSResult

func RequestReallocation(ctx context.Context, reason string) IMDSResult {
	client, transport := directClient()
	defer transport.CloseIdleConnections()
	return postReallocation(ctx, client, imdsURL, reason)
}

// URL/client injection stays private to this package's tests. Production always
// uses the link-local IMDS address; there is no endpoint override environment key.
func postReallocation(ctx context.Context, client *http.Client, endpoint, reason string) IMDSResult {
	if ctx.Err() != nil {
		return IMDSResult{Outcome: "not_sent"}
	}
	data, _ := json.Marshal(struct {
		Reason string `json:"reason"`
	}{reason})
	// NopCloser prevents GetBody replay; no idempotency key or POST retry loop.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, io.NopCloser(bytes.NewReader(data)))
	if err != nil {
		return IMDSResult{Outcome: "not_sent"}
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Metadata", "true")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return IMDSResult{Outcome: "uncertain"}
	}
	defer resp.Body.Close()
	result := IMDSResult{Outcome: "uncertain", Status: resp.StatusCode}
	switch {
	case resp.StatusCode == 204:
		result.Outcome = "accepted"
	case resp.StatusCode >= 300 && resp.StatusCode < 500 && resp.StatusCode != 408:
		result.Outcome = "rejected"
	}
	return result
}
