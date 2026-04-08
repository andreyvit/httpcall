package httpcall

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

var rateLimitBaseTime = time.Date(2022, 4, 1, 12, 0, 0, 0, time.UTC)

func rateLimitResponse(status int, remaining, reset string) *http.Response {
	h := http.Header{}
	if remaining != "" {
		h.Set("x-ratelimit-remaining", remaining)
	}
	if reset != "" {
		h.Set("x-ratelimit-reset", reset)
	}
	return &http.Response{StatusCode: status, Header: h}
}

func TestComputeDefaultRateLimitDelay_nilResponse(t *testing.T) {
	r := &Request{}
	if got := ComputeDefaultRateLimitDelay(r); got != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestComputeDefaultRateLimitDelay_missingHeaders(t *testing.T) {
	resp := rateLimitResponse(200, "", "")
	if got := computeDefaultRateLimitDelay(resp, rateLimitBaseTime, DefaultMinRateLimitDelay, DefaultRateLimitExtraBuffer, DefaultRateLimitFallbackDelay); got != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestComputeDefaultRateLimitDelay_remainingPositive(t *testing.T) {
	resp := rateLimitResponse(200, "5", strconv.FormatInt(rateLimitBaseTime.Unix()+1000, 10))
	if got := computeDefaultRateLimitDelay(resp, rateLimitBaseTime, DefaultMinRateLimitDelay, DefaultRateLimitExtraBuffer, DefaultRateLimitFallbackDelay); got != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestComputeDefaultRateLimitDelay_remainingZero_futureResetEpoch(t *testing.T) {
	resetTime := rateLimitBaseTime.Add(30 * time.Second)
	resp := rateLimitResponse(200, "0", strconv.FormatInt(resetTime.Unix(), 10))

	got := computeDefaultRateLimitDelay(resp, rateLimitBaseTime, DefaultMinRateLimitDelay, DefaultRateLimitExtraBuffer, DefaultRateLimitFallbackDelay)
	want := 31 * time.Second
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputeDefaultRateLimitDelay_remainingZero_pastResetEpoch(t *testing.T) {
	resetTime := rateLimitBaseTime.Add(-5 * time.Second)
	resp := rateLimitResponse(200, "0", strconv.FormatInt(resetTime.Unix(), 10))

	got := computeDefaultRateLimitDelay(resp, rateLimitBaseTime, DefaultMinRateLimitDelay, DefaultRateLimitExtraBuffer, DefaultRateLimitFallbackDelay)
	want := DefaultMinRateLimitDelay
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputeDefaultRateLimitDelay_remainingZero_withoutResetHeader(t *testing.T) {
	resp := rateLimitResponse(200, "0", "")

	got := computeDefaultRateLimitDelay(resp, rateLimitBaseTime, DefaultMinRateLimitDelay, DefaultRateLimitExtraBuffer, DefaultRateLimitFallbackDelay)
	want := DefaultRateLimitFallbackDelay
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputeDefaultRateLimitDelay_429_withoutHeaders(t *testing.T) {
	resp := rateLimitResponse(429, "", "")

	got := computeDefaultRateLimitDelay(resp, rateLimitBaseTime, DefaultMinRateLimitDelay, DefaultRateLimitExtraBuffer, DefaultRateLimitFallbackDelay)
	want := DefaultRateLimitFallbackDelay
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputeDefaultRateLimitDelay_429_withHeaders(t *testing.T) {
	resetTime := rateLimitBaseTime.Add(45 * time.Second)
	resp := rateLimitResponse(429, "0", strconv.FormatInt(resetTime.Unix(), 10))

	got := computeDefaultRateLimitDelay(resp, rateLimitBaseTime, DefaultMinRateLimitDelay, DefaultRateLimitExtraBuffer, DefaultRateLimitFallbackDelay)
	want := 46 * time.Second
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputeDefaultRateLimitDelay_malformedRemainingHeader(t *testing.T) {
	resp := rateLimitResponse(200, "not-a-number", strconv.FormatInt(rateLimitBaseTime.Unix()+1000, 10))
	if got := computeDefaultRateLimitDelay(resp, rateLimitBaseTime, DefaultMinRateLimitDelay, DefaultRateLimitExtraBuffer, DefaultRateLimitFallbackDelay); got != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestComputeDefaultRateLimitDelay_malformedResetHeaderFallsBack(t *testing.T) {
	resp := rateLimitResponse(200, "0", "not-a-timestamp")
	got := computeDefaultRateLimitDelay(resp, rateLimitBaseTime, DefaultMinRateLimitDelay, DefaultRateLimitExtraBuffer, DefaultRateLimitFallbackDelay)
	want := DefaultRateLimitFallbackDelay
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputeDefaultRateLimitDelay_retryAfterSeconds(t *testing.T) {
	resp := &http.Response{
		StatusCode: 429,
		Header:     http.Header{"Retry-After": {"30"}},
	}
	got := computeDefaultRateLimitDelay(resp, rateLimitBaseTime, DefaultMinRateLimitDelay, DefaultRateLimitExtraBuffer, DefaultRateLimitFallbackDelay)
	want := 31 * time.Second
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputeDefaultRateLimitDelay_retryAfterHTTPDate(t *testing.T) {
	when := rateLimitBaseTime.Add(10 * time.Second)
	resp := &http.Response{
		StatusCode: 429,
		Header:     http.Header{"Retry-After": {when.Format(time.RFC1123)}},
	}
	got := computeDefaultRateLimitDelay(resp, rateLimitBaseTime, DefaultMinRateLimitDelay, DefaultRateLimitExtraBuffer, DefaultRateLimitFallbackDelay)
	want := 11 * time.Second
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputeDefaultRateLimitDelay_retryAfterHTTPTimeFormat(t *testing.T) {
	when := rateLimitBaseTime.Add(10 * time.Second).UTC()
	resp := &http.Response{
		StatusCode: 429,
		Header:     http.Header{"Retry-After": {when.Format(http.TimeFormat)}},
	}
	got := computeDefaultRateLimitDelay(resp, rateLimitBaseTime, DefaultMinRateLimitDelay, DefaultRateLimitExtraBuffer, DefaultRateLimitFallbackDelay)
	want := 11 * time.Second
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputeDefaultRateLimitDelay_retryAfterRFC1123Z(t *testing.T) {
	when := rateLimitBaseTime.Add(10 * time.Second)
	resp := &http.Response{
		StatusCode: 429,
		Header:     http.Header{"Retry-After": {when.Format(time.RFC1123Z)}},
	}
	got := computeDefaultRateLimitDelay(resp, rateLimitBaseTime, DefaultMinRateLimitDelay, DefaultRateLimitExtraBuffer, DefaultRateLimitFallbackDelay)
	want := 11 * time.Second
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputeDefaultRateLimitDelay_ietfRateLimitHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("RateLimit-Remaining", "0")
	h.Set("RateLimit-Reset", "9")
	resp := &http.Response{
		StatusCode: 429,
		Header:     h,
	}
	got := computeDefaultRateLimitDelay(resp, rateLimitBaseTime, DefaultMinRateLimitDelay, DefaultRateLimitExtraBuffer, DefaultRateLimitFallbackDelay)
	want := 10 * time.Second
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputeDefaultRateLimitDelay_retryAfterOverridesResetHeader(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "7")
	h.Set("RateLimit-Reset", "30")
	resp := &http.Response{
		StatusCode: 429,
		Header:     h,
	}
	got := computeDefaultRateLimitDelay(resp, rateLimitBaseTime, DefaultMinRateLimitDelay, DefaultRateLimitExtraBuffer, DefaultRateLimitFallbackDelay)
	want := 8 * time.Second
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputeDefaultRateLimitDelay_resetAfterDeltaSeconds(t *testing.T) {
	h := http.Header{}
	h.Set("X-RateLimit-Reset-After", "30")
	resp := &http.Response{
		StatusCode: 429,
		Header:     h,
	}
	got := computeDefaultRateLimitDelay(resp, rateLimitBaseTime, DefaultMinRateLimitDelay, DefaultRateLimitExtraBuffer, DefaultRateLimitFallbackDelay)
	want := 31 * time.Second
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestComputeDefaultRateLimitDelay_resetUnixMilliseconds(t *testing.T) {
	when := rateLimitBaseTime.Add(45 * time.Second)
	h := http.Header{}
	h.Set("X-RateLimit-Reset", strconv.FormatInt(when.UnixMilli(), 10))
	resp := &http.Response{
		StatusCode: 429,
		Header:     h,
	}
	got := computeDefaultRateLimitDelay(resp, rateLimitBaseTime, DefaultMinRateLimitDelay, DefaultRateLimitExtraBuffer, DefaultRateLimitFallbackDelay)
	want := 46 * time.Second
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseRetryAfter_missingAndMalformed(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	if _, ok := parseRetryAfter(resp, rateLimitBaseTime); ok {
		t.Fatalf("expected ok=false")
	}

	resp.Header.Set("Retry-After", "-1")
	if _, ok := parseRetryAfter(resp, rateLimitBaseTime); ok {
		t.Fatalf("expected ok=false")
	}

	resp.Header.Set("Retry-After", "nonsense")
	if _, ok := parseRetryAfter(resp, rateLimitBaseTime); ok {
		t.Fatalf("expected ok=false")
	}
}

func TestParseRetryAfter_supportedFormats(t *testing.T) {
	t.Run("Seconds", func(t *testing.T) {
		resp := &http.Response{Header: http.Header{}}
		resp.Header.Set("Retry-After", "30")
		d, ok := parseRetryAfter(resp, rateLimitBaseTime)
		if !ok || d != 30*time.Second {
			t.Fatalf("d=%v ok=%v", d, ok)
		}
	})

	t.Run("HTTPTimeFormat", func(t *testing.T) {
		when := rateLimitBaseTime.Add(10 * time.Second).UTC()
		resp := &http.Response{Header: http.Header{}}
		resp.Header.Set("Retry-After", when.Format(http.TimeFormat))
		d, ok := parseRetryAfter(resp, rateLimitBaseTime)
		if !ok || d != 10*time.Second {
			t.Fatalf("d=%v ok=%v", d, ok)
		}
	})

	t.Run("RFC1123", func(t *testing.T) {
		when := rateLimitBaseTime.Add(10 * time.Second)
		resp := &http.Response{Header: http.Header{}}
		resp.Header.Set("Retry-After", when.Format(time.RFC1123))
		d, ok := parseRetryAfter(resp, rateLimitBaseTime)
		if !ok || d != 10*time.Second {
			t.Fatalf("d=%v ok=%v", d, ok)
		}
	})

	t.Run("RFC1123Z", func(t *testing.T) {
		when := rateLimitBaseTime.Add(10 * time.Second)
		resp := &http.Response{Header: http.Header{}}
		resp.Header.Set("Retry-After", when.Format(time.RFC1123Z))
		d, ok := parseRetryAfter(resp, rateLimitBaseTime)
		if !ok || d != 10*time.Second {
			t.Fatalf("d=%v ok=%v", d, ok)
		}
	})
}

func TestRequest_RateLimitExtraBuffer(t *testing.T) {
	resp := &http.Response{
		StatusCode: 429,
		Header:     http.Header{"Retry-After": {"30"}},
	}

	r := &Request{
		HTTPResponse:         resp,
		MinRateLimitDelay:    DefaultMinRateLimitDelay,
		RateLimitExtraBuffer: time.Nanosecond, // effectively "off"
	}
	got := ComputeDefaultRateLimitDelay(r)
	want := 30*time.Second + time.Nanosecond
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRequest_RateLimitExtraBuffer_DefaultAndNegative(t *testing.T) {
	resp := &http.Response{
		StatusCode: 429,
		Header:     http.Header{"Retry-After": {"1"}},
	}

	t.Run("Default", func(t *testing.T) {
		r := &Request{HTTPResponse: resp}
		got := ComputeDefaultRateLimitDelay(r)
		want := time.Second + DefaultRateLimitExtraBuffer
		if got != want {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("Negative", func(t *testing.T) {
		r := &Request{HTTPResponse: resp, RateLimitExtraBuffer: -time.Second}
		got := ComputeDefaultRateLimitDelay(r)
		want := time.Second + DefaultRateLimitExtraBuffer
		if got != want {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
}

func TestComputeDefaultRateLimitDelay_customFallbackDelay(t *testing.T) {
	resp := rateLimitResponse(429, "", "")
	got := computeDefaultRateLimitDelay(resp, rateLimitBaseTime, DefaultMinRateLimitDelay, DefaultRateLimitExtraBuffer, 7*time.Second)
	want := 7 * time.Second
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNoRateLimitDelay(t *testing.T) {
	if got := NoRateLimitDelay(&Request{}); got != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestRequest_Do_RateLimitDelayIsComputedBeforeFailed(t *testing.T) {
	var failedSawDelay time.Duration

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	r := &Request{
		CallID:      "RateLimited",
		Method:      http.MethodGet,
		Path:        srv.URL,
		MaxAttempts: 1,
		Failed: func(r *Request) {
			failedSawDelay = r.RateLimitDelay
			if r.Error == nil {
				t.Fatalf("expected error")
			}
			if r.Error.RetryDelay == 0 {
				t.Fatalf("expected Error.RetryDelay to be set")
			}
		},
	}

	err := r.Do()
	if err == nil || r.Error == nil {
		t.Fatalf("expected error")
	}
	if r.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("StatusCode=%d", r.StatusCode())
	}
	if !r.Error.IsRetriable {
		t.Fatalf("expected retriable")
	}
	if failedSawDelay == 0 {
		t.Fatalf("expected RateLimitDelay to be visible in Failed hook")
	}
}

func TestRequest_Do_MaxAllowedDelayPreventsRetry(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	r := &Request{
		CallID:          "RateLimitedLong",
		Method:          http.MethodGet,
		Path:            srv.URL,
		MaxAttempts:     3,
		MaxAllowedDelay: time.Second,
	}

	err := r.Do()
	if err == nil {
		t.Fatalf("expected error")
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	if r.Attempts != 1 {
		t.Fatalf("Attempts=%d", r.Attempts)
	}
}
