package httpcall

import (
	"net/http"
	"strconv"
	"time"
)

// DefaultRateLimitExtraBuffer is added to header-derived delays returned by
// ComputeDefaultRateLimitDelay.
//
// This is a pragmatic clock-skew / timing buffer: even if a server says "retry
// in 30 seconds", it's often safer to wait a bit longer to avoid immediately
// hitting the limit again.
//
// Note: a Request's RateLimitExtraBuffer uses 0 to mean "use the default".
// To effectively disable the buffer, set Request.RateLimitExtraBuffer to 1ns.
const DefaultRateLimitExtraBuffer = time.Second

// DefaultRateLimitFallbackDelay is used by ComputeDefaultRateLimitDelay when a
// response appears to be rate-limited (HTTP 429 or remaining==0), but none of
// the supported headers provide a concrete retry time.
//
// This is intentionally conservative: without a reset time, retrying too
// aggressively often causes tight 429 loops.
const DefaultRateLimitFallbackDelay = time.Minute

// DefaultMinRateLimitDelay is the default minimum delay returned by
// ComputeDefaultRateLimitDelay.
//
// This is a safety net against returning zero/negative delays when a server's
// rate-limit reset time is already in the past (clock skew, stale headers, etc).
const DefaultMinRateLimitDelay = time.Second

// DefaultMaxAllowedDelay is the default upper bound for retry sleeps.
//
// If a computed retry delay exceeds this value, Do() returns the error without
// sleeping/retrying. This avoids requests getting stuck behind very long retry
// windows (for example, 15-minute rate limits) when the caller would rather
// handle the situation explicitly.
const DefaultMaxAllowedDelay = 65 * time.Second

// NoRateLimitDelay is a ComputeRateLimitDelay implementation that disables
// rate-limit based delay computation.
//
// It always returns 0.
func NoRateLimitDelay(_ *Request) time.Duration {
	return 0
}

// ComputeDefaultRateLimitDelay computes a retry delay from common rate-limit
// headers on r.HTTPResponse.
//
// Supported signals:
//
//   - HTTP 429 (Too Many Requests)
//   - X-Ratelimit-Remaining / RateLimit-Remaining == 0
//   - Retry-After (seconds or HTTP date)
//   - X-Ratelimit-Reset (unix seconds, unix milliseconds, or delta seconds)
//   - X-Ratelimit-Reset-After (delta seconds)
//   - RateLimit-Remaining / RateLimit-Reset (IETF draft; treated as remaining==0 and delta seconds)
//
// The returned delay is at least Request.MinRateLimitDelay (or
// DefaultMinRateLimitDelay if unset) and includes Request.RateLimitExtraBuffer
// (or DefaultRateLimitExtraBuffer if unset). If the call appears to be
// rate-limited but no supported headers provide a concrete retry time, the
// returned delay falls back to Request.RateLimitFallbackDelay (or
// DefaultRateLimitFallbackDelay if unset).
//
// If the response is not considered rate-limited, the result is 0.
func ComputeDefaultRateLimitDelay(r *Request) time.Duration {
	resp := r.HTTPResponse
	if resp == nil {
		return 0
	}

	minDelay := r.MinRateLimitDelay
	if minDelay == 0 {
		minDelay = DefaultMinRateLimitDelay
	}

	extra := r.RateLimitExtraBuffer
	if extra < 0 {
		extra = 0
	}
	if extra == 0 {
		extra = DefaultRateLimitExtraBuffer
	}

	fallback := r.RateLimitFallbackDelay
	if fallback == 0 {
		fallback = DefaultRateLimitFallbackDelay
	}

	return computeDefaultRateLimitDelay(resp, time.Now(), minDelay, extra, fallback)
}

func computeDefaultRateLimitDelay(resp *http.Response, now time.Time, minDelay, extraBuffer, fallback time.Duration) time.Duration {
	isLimited := resp.StatusCode == http.StatusTooManyRequests
	if !isLimited {
		if remainingStr := headerGetAny(resp.Header, "X-Ratelimit-Remaining", "RateLimit-Remaining"); remainingStr != "" {
			remaining, err := strconv.Atoi(remainingStr)
			isLimited = (err == nil && remaining == 0)
		}
	}
	if !isLimited {
		return 0
	}

	if d, ok := parseRetryAfter(resp, now); ok {
		return max(minDelay, d+extraBuffer)
	}

	if d, ok := parseResetDelay(resp, now); ok {
		return max(minDelay, d+extraBuffer)
	}

	return max(minDelay, fallback)
}

func headerGetAny(h http.Header, keys ...string) string {
	for _, k := range keys {
		if v := h.Get(k); v != "" {
			return v
		}
	}
	return ""
}

func parseRetryAfter(resp *http.Response, now time.Time) (time.Duration, bool) {
	retryAfter := resp.Header.Get("Retry-After")
	if retryAfter == "" {
		return 0, false
	}
	if secs, err := strconv.ParseInt(retryAfter, 10, 64); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if when, err := http.ParseTime(retryAfter); err == nil {
		return when.Sub(now), true
	}
	if when, err := time.Parse(time.RFC1123, retryAfter); err == nil {
		return when.Sub(now), true
	}
	return 0, false
}

func parseResetDelay(resp *http.Response, now time.Time) (time.Duration, bool) {
	resetStr := headerGetAny(resp.Header,
		"X-Ratelimit-Reset-After",
		"RateLimit-Reset",
		"X-Ratelimit-Reset",
	)
	if resetStr == "" {
		return 0, false
	}

	resetNum, err := strconv.ParseInt(resetStr, 10, 64)
	if err != nil {
		return 0, false
	}

	// Heuristics:
	// - < 1e9: delta seconds (reset-after)
	// - >= 1e12: unix milliseconds
	// - otherwise: unix seconds
	switch {
	case resetNum < 1_000_000_000:
		return time.Duration(resetNum) * time.Second, true
	case resetNum >= 1_000_000_000_000:
		resetTime := time.UnixMilli(resetNum)
		return resetTime.Sub(now), true
	default:
		resetTime := time.Unix(resetNum, 0)
		return resetTime.Sub(now), true
	}
}
