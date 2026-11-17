package checks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

// recorded is one request as the fake API saw it.
type recorded struct {
	method, path string
	header       http.Header
	body         map[string]any
}

func fakeAPI(t *testing.T, status int, header map[string]string, response string) (*Client, *[]recorded) {
	t.Helper()
	var got []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not JSON: %v (%q)", err, raw)
		}
		got = append(got, recorded{r.Method, r.URL.Path, r.Header.Clone(), body})
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, Repo: "o/r", Token: "tok"}, &got
}

func TestCreateSendsRun(t *testing.T) {
	c, got := fakeAPI(t, http.StatusCreated, nil, `{"id":42}`)
	id, err := c.Create(context.Background(), Run{
		Name:    "jobs / a (linux/amd64)",
		HeadSHA: "abc",
		Status:  "in_progress",
		Output:  Output{Title: "running", Summary: "s"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != 42 {
		t.Fatalf("id = %d, want 42", id)
	}
	if len(*got) != 1 {
		t.Fatalf("requests = %d, want 1", len(*got))
	}
	r := (*got)[0]
	if r.method != "POST" || r.path != "/repos/o/r/check-runs" {
		t.Fatalf("request = %s %s", r.method, r.path)
	}
	for k, want := range map[string]string{
		"Authorization":        "Bearer tok",
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
		"User-Agent":           "jobs-action",
	} {
		if g := r.header.Get(k); g != want {
			t.Errorf("header %s = %q, want %q", k, g, want)
		}
	}
	want := map[string]any{
		"name":     "jobs / a (linux/amd64)",
		"head_sha": "abc",
		"status":   "in_progress",
		"output":   map[string]any{"title": "running", "summary": "s"},
	}
	if !reflect.DeepEqual(r.body, want) {
		t.Fatalf("body = %v\nwant   %v", r.body, want)
	}
}

func TestUpdateCompleted(t *testing.T) {
	c, got := fakeAPI(t, http.StatusOK, nil, `{"id":42}`)
	err := c.Update(context.Background(), 42, Run{
		Name:       "ignored on update",
		HeadSHA:    "ignored on update",
		Status:     "completed",
		Conclusion: "failure",
		Output:     Output{Title: "failed", Summary: "s", Text: "boom"},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := (*got)[0]
	if r.method != "PATCH" || r.path != "/repos/o/r/check-runs/42" {
		t.Fatalf("request = %s %s", r.method, r.path)
	}
	want := map[string]any{
		"status":     "completed",
		"conclusion": "failure",
		"output":     map[string]any{"title": "failed", "summary": "s", "text": "boom"},
	}
	if !reflect.DeepEqual(r.body, want) {
		t.Fatalf("body = %v\nwant   %v", r.body, want)
	}
}

func TestErrorClassification(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		header      map[string]string
		permission  bool
		rateLimited bool
		retryAfter  time.Duration
	}{
		{"bare 403", 403, nil, true, false, 0},
		{"404", 404, nil, true, false, 0},
		{"401", 401, nil, true, false, 0},
		{"403 primary rate limit", 403, map[string]string{"X-RateLimit-Remaining": "0"}, false, true, 0},
		{"429 retry-after", 429, map[string]string{"Retry-After": "7"}, false, true, 7 * time.Second},
		{"403 secondary rate limit", 403, map[string]string{"Retry-After": "60"}, false, true, 60 * time.Second},
		{"500", 500, nil, false, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := fakeAPI(t, tc.status, tc.header, `{"message":"nope"}`)
			_, err := c.Create(context.Background(), Run{Name: "n", HeadSHA: "s", Status: "in_progress"})
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if e.Status != tc.status || e.Permission() != tc.permission || e.RateLimited != tc.rateLimited || e.RetryAfter != tc.retryAfter {
				t.Fatalf("got status=%d permission=%v rateLimited=%v retryAfter=%v", e.Status, e.Permission(), e.RateLimited, e.RetryAfter)
			}
			if e.Message != "nope" {
				t.Fatalf("message = %q, want nope", e.Message)
			}
		})
	}
}

func TestErrorMessageFallsBackToBody(t *testing.T) {
	c, _ := fakeAPI(t, 502, nil, "<html>bad gateway</html>")
	err := c.Update(context.Background(), 1, Run{Status: "completed", Conclusion: "success"})
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want *Error", err)
	}
	if e.Message != "<html>bad gateway</html>" {
		t.Fatalf("message = %q", e.Message)
	}
}
