// Package desec is a small client for the deSEC API (https://desec.io/api/v1).
// It covers only what the provider uses: domains, RRsets, tokens and token
// policies.
package desec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the production API.
const DefaultBaseURL = "https://desec.io/api/v1"

// Client calls the deSEC API with one token.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	// UserAgent goes in the User-Agent header.
	UserAgent string
	// MaxRetries is the number of retries after a 429 response.
	MaxRetries int
	// MaxWait is the longest wait for one Retry-After value.
	MaxWait time.Duration
	// sleep is replaced in tests.
	sleep func(context.Context, time.Duration) error
}

// New returns a client with defaults for the empty fields.
func New(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Token:      token,
		HTTP:       &http.Client{Timeout: 60 * time.Second},
		UserAgent:  "terraform-provider-desec",
		MaxRetries: 8,
		MaxWait:    5 * time.Minute,
		sleep:      sleepCtx,
	}
}

// APIError is a response with a status code of 400 or more.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("deSEC API: %s %s: HTTP %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// IsNotFound reports whether err is a 404 response.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// retryAfter reads the Retry-After header in seconds. deSEC sends seconds.
func retryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return time.Second
	}
	if s, err := strconv.ParseFloat(v, 64); err == nil && s >= 0 {
		return time.Duration(s*float64(time.Second)) + 100*time.Millisecond
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return time.Second
}

// do sends a request. A nil in is no body. A nil out discards the response.
// It returns the status code. A status of 400 or more is an *APIError.
func (c *Client) do(ctx context.Context, method, path string, in, out any) (int, error) {
	var payload []byte
	if in != nil {
		var err error
		payload, err = json.Marshal(in)
		if err != nil {
			return 0, fmt.Errorf("encode request: %w", err)
		}
	}

	for attempt := 0; ; attempt++ {
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Token "+c.Token)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.UserAgent != "" {
			req.Header.Set("User-Agent", c.UserAgent)
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return 0, err
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		_ = resp.Body.Close()
		if err != nil {
			return resp.StatusCode, fmt.Errorf("read response: %w", err)
		}

		if resp.StatusCode == http.StatusTooManyRequests && attempt < c.MaxRetries {
			wait := retryAfter(resp.Header)
			if c.MaxWait > 0 && wait > c.MaxWait {
				wait = c.MaxWait
			}
			if err := c.sleep(ctx, wait); err != nil {
				return resp.StatusCode, err
			}
			continue
		}

		if resp.StatusCode >= 400 {
			return resp.StatusCode, &APIError{
				Method:     method,
				Path:       path,
				StatusCode: resp.StatusCode,
				Body:       strings.TrimSpace(string(data)),
			}
		}
		if out != nil && len(data) > 0 && resp.StatusCode != http.StatusNoContent {
			if err := json.Unmarshal(data, out); err != nil {
				return resp.StatusCode, fmt.Errorf("decode response of %s %s: %w", method, path, err)
			}
		}
		return resp.StatusCode, nil
	}
}
