// Package checks is the part of GitHub's Checks API jobs-action needs: create
// a check run, update it, and tell a rate limit from a missing permission.
// Two endpoints do not justify a client library.
package checks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MaxOutput is GitHub's limit, in characters, on a check run's output summary
// and on its output text.
const MaxOutput = 65535

// Output is a check run's report body.
type Output struct{ Title, Summary, Text string }

// Run is a check run as jobs-action writes it.
type Run struct {
	Name, HeadSHA string // create only; Update never sends them
	Status        string // "in_progress" | "completed"
	Conclusion    string // "success" | "failure" | "cancelled"; completed only
	Output        Output
}

// Client talks to one repository's check runs.
type Client struct {
	BaseURL string       // GITHUB_API_URL, no trailing slash
	Repo    string       // owner/name
	Token   string       // the workflow's token, or a GitHub App installation token
	HTTP    *http.Client // nil = a client with a 10s timeout
}

// Error is a non-2xx answer from the API.
type Error struct {
	Status int
	// RateLimited marks a 403 or 429 that carries Retry-After or
	// X-RateLimit-Remaining: 0. That is how a rate limit is told apart from a
	// 403 for a missing permission.
	RateLimited bool
	RetryAfter  time.Duration // 0 when the response named none
	Message     string        // the response's "message", else the start of its body
}

func (e *Error) Error() string {
	what := "GitHub API"
	if e.RateLimited {
		what = "GitHub API rate limit"
	}
	if e.Message == "" {
		return fmt.Sprintf("%s: HTTP %d", what, e.Status)
	}
	return fmt.Sprintf("%s: HTTP %d: %s", what, e.Status, e.Message)
}

// Permission reports whether the token may not write check runs here: the
// workflow did not grant "checks: write", or the token is not an App token.
func (e *Error) Permission() bool {
	if e.RateLimited {
		return false
	}
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden || e.Status == http.StatusNotFound
}

var defaultHTTP = &http.Client{Timeout: 10 * time.Second}

// payload is the JSON body of both calls.
type payload struct {
	Name       string         `json:"name,omitempty"`
	HeadSHA    string         `json:"head_sha,omitempty"`
	Status     string         `json:"status"`
	Conclusion string         `json:"conclusion,omitempty"`
	Output     *payloadOutput `json:"output,omitempty"`
}

type payloadOutput struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Text    string `json:"text,omitempty"`
}

func newPayload(r Run) payload {
	p := payload{Status: r.Status}
	if r.Status == "completed" {
		p.Conclusion = r.Conclusion
	}
	if r.Output != (Output{}) {
		p.Output = &payloadOutput{Title: r.Output.Title, Summary: r.Output.Summary, Text: r.Output.Text}
	}
	return p
}

// Create starts a check run on r.HeadSHA and returns its id.
func (c *Client) Create(ctx context.Context, r Run) (int64, error) {
	p := newPayload(r)
	p.Name, p.HeadSHA = r.Name, r.HeadSHA
	var out struct {
		ID int64 `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/repos/"+c.Repo+"/check-runs", p, &out); err != nil {
		return 0, err
	}
	if out.ID == 0 {
		return 0, fmt.Errorf("GitHub API: create check run: the response carried no id")
	}
	return out.ID, nil
}

// Update changes the status, conclusion and output of check run id.
func (c *Client) Update(ctx context.Context, id int64, r Run) error {
	return c.do(ctx, http.MethodPatch, "/repos/"+c.Repo+"/check-runs/"+strconv.FormatInt(id, 10), newPayload(r), nil)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "jobs-action")
	req.Header.Set("Content-Type", "application/json")

	client := c.HTTP
	if client == nil {
		client = defaultHTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub API: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newError(resp, respBody)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("GitHub API: %s %s: decode response: %w", method, path, err)
	}
	return nil
}

func newError(resp *http.Response, body []byte) *Error {
	e := &Error{Status: resp.StatusCode}
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n >= 0 {
			e.RetryAfter = time.Duration(n) * time.Second
		}
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		e.RateLimited = resp.Header.Get("Retry-After") != "" || resp.Header.Get("X-RateLimit-Remaining") == "0"
	}
	var msg struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &msg) == nil && msg.Message != "" {
		e.Message = msg.Message
	} else {
		e.Message = strings.TrimSpace(string(body[:min(len(body), 200)]))
	}
	return e
}
