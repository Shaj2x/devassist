package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// HTTPOptions bounds every provider call.
type HTTPOptions struct {
	Timeout        time.Duration // per attempt (default 60s)
	MaxRetries     int           // retries after the first attempt (default 4)
	InitialBackoff time.Duration // default 1s, doubled per retry with jitter
}

func (o HTTPOptions) withDefaults() HTTPOptions {
	if o.Timeout <= 0 {
		o.Timeout = 60 * time.Second
	}
	if o.MaxRetries < 0 {
		o.MaxRetries = 0
	}
	if o.InitialBackoff <= 0 {
		o.InitialBackoff = time.Second
	}
	return o
}

// apiError is a non-2xx response.
type apiError struct {
	status     int
	body       string
	retryAfter time.Duration
}

func (e *apiError) Error() string {
	return fmt.Sprintf("embedding API returned %d: %s", e.status, e.body)
}

// retryable: rate limits, server errors, and network failures. A 400 or 401
// will not get better by retrying.
func retryable(err error) bool {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.status == http.StatusTooManyRequests || ae.status >= 500
	}
	return !errors.Is(err, context.Canceled)
}

// postJSON sends body to url and decodes the response into out, retrying
// transient failures with exponential backoff (honouring Retry-After).
func postJSON(ctx context.Context, client *http.Client, opts HTTPOptions, url, apiKey string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	backoff := opts.InitialBackoff
	for attempt := 0; ; attempt++ {
		err := postOnce(ctx, client, opts.Timeout, url, apiKey, payload, out)
		if err == nil {
			return nil
		}
		if attempt >= opts.MaxRetries || !retryable(err) || ctx.Err() != nil {
			return err
		}
		// Jitter spreads retries from parallel workers; it needs no crypto randomness.
		wait := backoff + time.Duration(rand.Int64N(int64(backoff)/2+1)) //nolint:gosec
		var ae *apiError
		if errors.As(err, &ae) && ae.retryAfter > 0 {
			wait = ae.retryAfter
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		backoff *= 2
	}
}

func postOnce(ctx context.Context, client *http.Client, timeout time.Duration, url, apiKey string, payload []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		ae := &apiError{status: resp.StatusCode, body: truncate(string(data), 300)}
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
			ae.retryAfter = time.Duration(secs) * time.Second
		}
		return ae
	}
	return json.Unmarshal(data, out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
