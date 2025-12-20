# httpcall

[![Go reference](https://pkg.go.dev/badge/github.com/andreyvit/httpcall.svg)](https://pkg.go.dev/github.com/andreyvit/httpcall) ![Zero dependencies](https://img.shields.io/badge/deps-zero-brightgreen) [![Go Report Card](https://goreportcard.com/badge/github.com/andreyvit/httpcall)](https://goreportcard.com/report/github.com/andreyvit/httpcall)

Go library for making typical non-streaming single-roundtrip HTTP API calls without boilerplate.

Used in production for years.

## Usage

Install:

```sh
go get github.com/andreyvit/httpcall@latest
```

### Basic call

```go
var out SomeResponse
err := (&httpcall.Request{
	CallID:    "ListWidgets",
	Method:    http.MethodGet,
	BaseURL:   "https://api.example.com",
	Path:      "/v1/widgets",
	QueryParams: url.Values{
		"limit": {"100"},
	},
	OutputPtr:   &out,
	MaxAttempts: 3,
}).Do()
if err != nil {
	return err
}
```

URL building:

- Use `BaseURL` + `Path` (most common), or set `Path` to a full URL (when it contains `://`).
- Use `PathParams` to substitute placeholders in `Path` (values are URL path-escaped).
- Use `FullURLOverride` to follow REST-style links (absolute URL or an absolute path starting with `/`).

Request input/output:

- `Input` defaults to JSON request body (`application/json`).
- If `Input` is `url.Values`, request body is form-encoded (`application/x-www-form-urlencoded; charset=utf-8`).
- If `OutputPtr` is `*[]byte`, the response body is returned as raw bytes (no JSON parsing).
- Otherwise, `OutputPtr` is unmarshaled from JSON response body.

### Rate limiting and retries

If you leave everything at defaults and set `MaxAttempts > 1`, `httpcall` will:

- retry failed requests automatically;
- obey common rate-limiting headers automatically (up to a bounded sleep duration);
- expose the computed delay so you can also use it externally for throttling.

You can read `r.RateLimitDelay` to throttle future requests; this field is computed even after a successful response.


#### When does `httpcall` retry?

`Do()` retries only when all of these are true:

- the last attempt produced an error (`r.Error != nil`);
- `r.Error.IsRetriable` is true (by default, this means network errors, HTTP 5xx and HTTP 429);
- `r.Attempts < r.MaxAttempts`.

HTTP 429 (Too Many Requests) is retriable even for non-idempotent methods
(POST/PUT/PATCH/DELETE), because the server is telling you it did not accept the
request due to throttling. HTTP 5xx is not, because we do not know if the server
started executing the request before failing.


#### How does retry delay work?

- When an error response is encountered, `r.ParseErrorResponse` hook is called to populate `r.Error` details; it can set `r.Error.RetryDelay` among other fields. Note that if you set a non-zero retry delay at this stage, it will be used without any further adjustments.
- After each attempt finishes, `httpcall` computes `r.RateLimitDelay` using `r.ComputeRateLimitDelay` hook (defaults to `httpcall.ComputeDefaultRateLimitDelay`).
- If `r.Error.RetryDelay` is still zero, it will be initialized to `r.RateLimitDelay` or `r.RetryDelay` or `httpcall.DefaultRetryDelay`, whichever is non-zero. This is where rate limit delay gets applied.
- Then `Failed` hook runs, and it is free to adjust both `r.Error.IsRetriable` and `r.Error.RetryDelay`.
- If the error is non-retriable, or max attempts have been reached, or the resulting `r.Error.RetryDelay` is more than `r.MaxAllowedDelay` (defaults to `httpcall.DefaultMaxAllowedDelay` = 65s), `Do()` returns the error immediately without sleeping or retrying.
- Otherwise, `Do()` sleeps for `r.Error.RetryDelay` and retries.


#### How rate limiting is detected

`ComputeDefaultRateLimitDelay` treats a response as rate-limited when either:

- status is HTTP 429, or
- `RateLimit-Remaining` / `X-RateLimit-Remaining` / `X-Ratelimit-Remaining` is present and equals `0`.

The second case is important: many APIs indicate “you are out of quota” via remaining=0 without returning 429, and sometimes without providing a retry time. In that situation, `httpcall` still sets `r.RateLimitDelay` to a non-zero value:

- It uses `Retry-After` or `X-RateLimit-Reset*` headers when available.
- If no usable reset information is present, it falls back to a conservative delay (`r.RateLimitFallbackDelay`, default `httpcall.DefaultRateLimitFallbackDelay` = 1m).

This means you can safely throttle future work after a successful call by checking `r.RateLimitDelay`, if you want to.


#### Using the delay externally

The same rate-limit computation powers three useful patterns:

1. **Built-in retries**: `Do()` uses `r.Error.RetryDelay` (which defaults to `r.RateLimitDelay`) when retrying.
2. **External rescheduling on errors**: if you run with `MaxAttempts=1` or ran out of attempts, you can still read the suggested delay from the returned `*httpcall.Error` (`RetryDelay`) and reschedule your job accordingly.
3. **Throttling after success**: `r.RateLimitDelay` is computed even when `Do()` succeeds, so you can optionally slow down the next request when you run out of quota.


#### Example: exponential backoff

You can implement your own backoff policy using `OnFailed`:

```go
delays := time.Duration{time.Second, 4*time.Second, 16*time.Second, 1*time.Minute}

r.OnFailed(func(r *httpcall.Request) {
	next := delays[min(r.Attempts-1, len(delays)-1)]
	r.Error.RetryDelay = max(next, r.RateLimitDelay)
})
```

#### Rate limiting adjustments

- Disable rate-limit delay computation: `r.ComputeRateLimitDelay = httpcall.NoRateLimitDelay`.
- Replace it completely: set `r.ComputeRateLimitDelay` to your own function.
- Tune rate-limit parsing: `MinRateLimitDelay`, `RateLimitExtraBuffer` (a clock skew buffer added to server-reported time), `RateLimitFallbackDelay` (a delay to use when server doesn't provide a usable one), `MaxAllowedDelay`.


### Composable configuration

The intended way to use `httpcall` in larger codebases is to build requests locally, then let the outer environment configure them (HTTP client, logging, retry rules, auth, throttling, etc).

All lifecycle hooks are composable: instead of overwriting `r.Started`, `r.Failed`, etc, use `r.OnStarted`, `r.OnFailed`, `r.OnFinished`, `r.OnShouldStart`, and `r.OnValidate` to add behavior without clobbering whatever was configured before.

This is a real pattern from a large production codebase (simplified):

```go
func (rc *RC) ConfigureHTTPRequest(r *httpcall.Request) {
	if r.HTTPClient == nil {
		r.HTTPClient = rc.APIClient
	}

	r.OnStarted(func(r *httpcall.Request) {
		log.Printf("%s: %s %s", r.CallID, r.Method, r.Curl())
	})

	r.OnFinished(func(r *httpcall.Request) {
		if r.Error == nil {
			log.Printf("%s -> HTTP %d [%d ms]", r.CallID, r.StatusCode(), r.Duration.Milliseconds())
		} else {
			log.Printf("%s -> %s", r.CallID, r.Error.ShortError())
		}
	})
}
```

### Error handling

`Do()` returns `*httpcall.Error` (which wraps the underlying cause), so you can inspect details:

```go
err := r.Do()
var hcErr *httpcall.Error
if errors.As(err, &hcErr) {
	_ = hcErr.StatusCode
	_ = hcErr.RawResponseBody
	_ = hcErr.IsNetwork
}
```

### Debugging with `curl`

`r.Curl()` formats the request as a runnable curl command. This is used heavily for logging and debugging production issues.

### Response size limit

Response body reading is bounded by `MaxResponseLength` (defaults to `httpcall.DefaultMaxResponseLength`). When exceeded, `Do()` fails with `httpcall.ErrResponseTooLong`.

## License

Copyright 2025, Andrey Tarantsov.
Published under the terms of the [MIT license](LICENSE).
