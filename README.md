# httpcall

[![Go reference](https://pkg.go.dev/badge/github.com/andreyvit/httpcall.svg)](https://pkg.go.dev/github.com/andreyvit/httpcall) ![Zero dependencies](https://img.shields.io/badge/deps-zero-brightgreen) [![Go Report Card](https://goreportcard.com/badge/github.com/andreyvit/httpcall)](https://goreportcard.com/report/github.com/andreyvit/httpcall)

Go library for making typical non-streaming single-roundtrip HTTP API calls. Yes, really.

Status: Code used in production for years. Rate limiting stuff is new, though.


## Why?

Normally, Go apps are supposed to avoid stupid dependencies like this one.

This library saves you from four sins, however:

- poor networking behaviors due to laziness (lack of retries or no rate limiting — networking is an area where “good enough for now” is a far cry from “good”)
- unhelpful logs (why on earth did that call fail and what did it even say?)
- subtle vulnerabilities (did you forget to limit response length?)
- hard to extract common HTTP configuration across multiple subsystems

...and, of course, some would find it appealing just to avoid pasting the same 50-line HTTP helper again.


## Features

- makes simple things simple and hard things possible
- makes your calling code easy to read
- makes your HTTP configuration composable and centralizable
- makes error handling easy
- retries on failures if you allow
- handles typical HTTP rate limiting headers (and you can plug in your own)


## Usage

Installation:

```sh
go get github.com/andreyvit/httpcall@latest
```

Basic usage:

```go
var out SomeResponse
r := &httpcall.Request{
	CallID:    "ListWidgets",
	Method:    http.MethodGet,
	BaseURL:   "https://api.example.com",
	Path:      "/v1/widgets",
	QueryParams: url.Values{
		"limit": {"100"},
	},
	OutputPtr:   &out,
	MaxAttempts: 3,
}
err := r.Do()
if err != nil {
	return err
}
```


### Composable configuration

The intended way to use httpcall is to build requests locally, then let the common code and outer environment configure them (HTTP client, logging, retry rules, auth, throttling, etc).

All lifecycle hooks are composable: instead of overwriting `r.Started`, `r.Failed`, etc, use `r.OnStarted`, `r.OnFailed`, `r.OnFinished`, `r.OnShouldStart`, and `r.OnValidate` to add behavior without clobbering whatever was configured before.

Example:

```go
func (app *MyApp) ConfigureHTTPRequest(r *httpcall.Request) {
	if r.HTTPClient == nil {
		r.HTTPClient = app.APIClient
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

You would typically define a subsystem-local configuration function which sets `BaseURL`, `ParseErrorResponse`, auth headers and the like, and calls system-wide configuration function:

```go
func (svc *SomeService) ConfigureHTTPRequest(r *httpcall.Request) {
	r.BaseURL = "https://api.example.com/v1"
	r.ParseErrorResponse = parseSomeServiceErrorResponse
	svc.app.ConfigureHTTPRequest(r)
}
```

and then specific calls would be like:

```go
func (svc *SomeService) listWhatever() ([]*Whatever, error) {
	var whatevers []*Whatever
	r := &httpcall.Request{
		CallID:  "ListWhatever",
		Method:  http.MethodGet,
		Path:    "/whatevers",
		QueryParams: url.Values{
			"limit": {"100"},
		},
		OutputPtr: &whatevers,
		MaxAttempts: 3,
	}
	svc.ConfigureHTTPRequest(r)
	err := r.Do()
	return whatevers, err
}
```


### Building URLs

Use `BaseURL` and `Path` for convenience, or just provide the full URL in `Path`. Use `QueryParams` to set the query string:

```go
r := &httpcall.Request{
	Method:  http.MethodGet,
	BaseURL: "https://api.example.com/api",
	Path:    "/v1/widgets",
	QueryParams: url.Values{
		"limit": {"100"},
	},
}
// -> https://api.example.com/api/v1/widgets?limit=100
```

If `Path` contains `://`, it is treated as a full URL and `BaseURL` is ignored. If can still pass `QueryParams`.

Use `PathParams` to substitute values in the path with proper escaping:

```go
r := &httpcall.Request{
	Method:  http.MethodGet,
	BaseURL: "https://api.example.com",
	Path:    "/v1/widgets/{id}",
	PathParams: map[string]string{
		"{id}": "hello/world",
	},
}
// -> https://api.example.com/v1/widgets/hello%252Fworld
```

Use `FullURLOverride` for REST-style links like `next`/`prev`:

- If `FullURLOverride` contains `://`, it fully overrides the URL (`BaseURL`, `Path` and `QueryParams` are ignored).
- Otherwise, it must start with `/` and replaces only the URL path (scheme/host are taken from `BaseURL`/`Path`, and `QueryParams` are preserved).


### Input

If `RawRequestBody` is non-nil, it is sent as-is and `Input` is ignored. Otherwise, `Input` is encoded to populate `RawRequestBody` and set a default `Content-Type`.


#### JSON input

```go
r := &httpcall.Request{
	CallID:    "CreateWidget",
	Method:    http.MethodPost,
	BaseURL:   "https://api.example.com",
	Path:      "/v1/widgets",
	Input:     &CreateWidgetRequest{Name: "hello"},
	OutputPtr: &out,
}
```

#### Form input (`url.Values`)

```go
r := &httpcall.Request{
	Method: http.MethodPost,
	Path:   "https://example.com/oauth/token",
	Input: url.Values{
		"grant_type": {"client_credentials"},
	},
}
```

#### Binary/raw input

```go
r := &httpcall.Request{
	CallID:                 "CreateWidget",
	Method:                 http.MethodPost,
	BaseURL:                "https://api.example.com",
	Path:                   "/v1/widgets",
	RawRequestBody:         []byte{1, 2, 3},
	RequestBodyContentType: "application/octet-stream",
	OutputPtr:              &out,
}
```


### Output

httpcall always reads the body into `r.RawResponseBody` (bounded by `MaxResponseLength`).

A successful 2xx response is then parsed by `r.ParseResponse` if set, or via built-in logic if not. Built-in logic handles `OutputPtr` set to nil (ignored), `*[]byte` (stores raw bytes output), or a pointer to JSON-compatible data (unmarshals JSON).

A failed (non-2xx) response is parsed by `r.ParseErrorResponse` if set.


#### JSON output

```go
var out SomeResponse
err := (&httpcall.Request{
	Method:    http.MethodGet,
	Path:      "https://api.example.com/v1/widgets",
	OutputPtr: &out,
}).Do()
```

#### Raw bytes output

```go
var body []byte
err := (&httpcall.Request{
	Method:    http.MethodGet,
	Path:      "https://example.com/file.bin",
	OutputPtr: &body,
}).Do()
```

#### Ignore output

```go
err := (&httpcall.Request{
	Method: http.MethodPost,
	Path:   "https://example.com/v1/widgets/123/refresh",
}).Do()
```

#### Custom parsing

```go
var text string
err := (&httpcall.Request{
	Method: http.MethodGet,
	Path:   "https://example.com/healthz",
	ParseResponse: func(r *httpcall.Request) error {
		text = string(r.RawResponseBody)
		return nil
	},
}).Do()
```


#### Parsing error responses

When the server responds with a non-2xx status code, httpcall returns `*httpcall.Error`.

If `ParseErrorResponse` is set, it runs right after the response body has been
read and before retry-delay initialization and before the `Failed` hook. It can:

- decode `r.RawResponseBody`;
- populate `r.Error.Type` and `r.Error.Message` for better logs/errors;
- adjust `r.Error.IsRetriable` and/or set `r.Error.RetryDelay`;
- set `r.Error.PrintResponseBody` to false to suppress a sensitive or long body when logging the error;
- attach application-defined error categories (see below);
- suppress the error entirely by setting `r.Error = nil` (use sparingly).

Example:

```go
var InvalidEmailCategory = &httpcall.ErrorCategory{Name: "invalid_email"}

func parseSomeAPIErrorResponse(r *httpcall.Request) {
	var resp struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(r.RawResponseBody, &resp) == nil {
		r.Error.Type = resp.Code
		r.Error.Message = resp.Message
	}
	switch r.Error.Type {
	case "server_overloaded":
		r.Error.IsRetriable = true
		r.Error.RetryDelay = 10 * time.Second
	case "invalid_email":
		r.Error.IsRetriable = false
		r.Error.AddCategory(InvalidEmailCategory)
	}
}

r := &httpcall.Request{
	Method:             http.MethodGet,
	Path:               "https://api.example.com/v1/widgets",
	ParseErrorResponse:  parseSomeAPIErrorResponse,
}
err := r.Do()
```


#### Error categories

Error categories are a lightweight way to attach application-defined classification to an `*httpcall.Error`.

Key properties:

- Categories are compared by identity (pointer), so you typically define them as package-level globals.
- A category can be checked with `errors.Is(err, MyCategory)` (because `*httpcall.Error` implements a custom `Is` method).
- Only two categories are supported per error (`Error.AddCategory` panics on a third), so keep them high-signal.

Example:

```go
var InvalidEmailCategory = &httpcall.ErrorCategory{Name: "invalid_email"}

err := r.Do()

if errors.Is(err, InvalidEmailCategory) {
	// Handle as a user / data problem.
}
```


#### Validating output

`ValidateOutput` runs after a successful response has been parsed and lets you fail the call with a domain-specific error.

Example:

```go
var out struct {
	ID string `json:"id"`
}
err := (&httpcall.Request{
	Method:    http.MethodGet,
	Path:      "https://api.example.com/v1/widgets/123",
	OutputPtr: &out,
	ValidateOutput: func() error {
		if out.ID == "" {
			return fmt.Errorf("missing id in response")
		}
		return nil
	},
}).Do()
```


### Error type: `httpcall.Error`

If `Do()` returns an error, it is always `*httpcall.Error`, so you can access all the details.

```go
err := r.Do()
if err != nil {
	var e *httpcall.Error
	if errors.As(err, &e) {
		_ = e.StatusCode
		_ = e.RawResponseBody
		_ = e.IsNetwork
		_ = e.IsRetriable
		_ = e.RetryDelay
	}
}
```

#### External sleep/reschedule via `RetryDelay`

If you want httpcall to *compute* a backoff delay but not to sleep/retry by itself,
set `MaxAttempts=1` and use `Error.RetryDelay`:

```go
r.MaxAttempts = 1
err := r.Do()

var e *httpcall.Error
if errors.As(err, &e) && e.IsRetriable && e.RetryDelay > 0 {
	time.Sleep(e.RetryDelay) // or reschedule a job instead of sleeping
}
```

`Error.RetryDelay` is initialized by `Do()` before calling the `Failed` hook, so hooks can safely read and adjust it.

#### Logging (including error responses)

`*httpcall.Error` is designed to be log-friendly:

- `e.ShortError()` omits CallID and status code (useful when you already log those separately).
- `e.Error()` includes CallID/status and, when `PrintResponseBody` is true, includes the raw response body inline.
- `e.RawResponseBody` is always available if you prefer structured logging.

Example:

```go
if err := r.Do(); err != nil {
	e := err.(*httpcall.Error)
	log.Printf("%s -> %s (retry=%v) curl=%s", r.CallID, e.Error(), e.RetryDelay, r.Curl())
}
```

Be mindful of secrets: if responses can contain credentials/PII, you may want to set `e.PrintResponseBody = false` in `ParseErrorResponse` or `Failed`.

#### Checking categories

If you attach categories (see above), you can branch on them:

```go
if errors.Is(err, InvalidEmailCategory) {
	// ...
}
```

#### Custom final logs via `OnFinished`

`OnFinished` runs exactly once per `Do()` call and is often the cleanest place to emit
logs because it has access to everything: status code, duration, curl, retry delay,
and error response body.

```go
r.OnFinished(func(r *httpcall.Request) {
	if r.Error == nil {
		log.Printf("%s -> HTTP %d [%d ms]", r.CallID, r.StatusCode(), r.Duration.Milliseconds())
		return
	}
	log.Printf("%s -> HTTP %d [%d ms] retry=%v err=%s",
		r.CallID,
		r.StatusCode(),
		r.Duration.Milliseconds(),
		r.Error.RetryDelay,
		r.Error.Error(),
	)
})
```


### Retries and rate limiting

Set `MaxAttempts` to more than 1 to enable automatic retries. Set `RetryDelay` to adjust the delay between retries, or use `OnFailed` hook for custom retry logic.

`Do()` retries a failed request (`r.Error != nil`) if `r.Error.IsRetriable` is true (by default, this means network errors, HTTP 5xx and HTTP 429), and `r.Attempts < r.MaxAttempts`. By default, `IsRetriable` is set to true for network errors, HTTP 5xx on idempotent requests, and HTTP 429.

HTTP 429 (Too Many Requests) is retriable even for non-idempotent methods
(POST/PUT/PATCH/DELETE), because the server is telling you it did not accept the
request due to throttling. HTTP 5xx is not, because we do not know if the server
started executing the request before failing.

You can adjust which requests are retried and the delays used by setting `r.IsRetriable` in a `Failed` hook.

Retry timeline:

1. When an error response is encountered, `r.ParseErrorResponse` hook is called to populate `r.Error` details; it can set `r.Error.RetryDelay` among other fields. Note that if you set a non-zero retry delay at this stage, it will be used without any further adjustments.
2. After each attempt finishes, httpcall computes `r.RateLimitDelay` using `r.ComputeRateLimitDelay` hook (defaults to `httpcall.ComputeDefaultRateLimitDelay`).
3. If `r.Error.RetryDelay` is still zero, it will be initialized to `r.RateLimitDelay` or `r.RetryDelay` or `httpcall.DefaultRetryDelay`, whichever is non-zero. This is where rate limit delay gets applied.
4. Then `Failed` hook runs, and it is free to adjust both `r.Error.IsRetriable` and `r.Error.RetryDelay`.
5. If the error is non-retriable, or max attempts have been reached, or the resulting `r.Error.RetryDelay` is more than `r.MaxAllowedDelay` (defaults to `httpcall.DefaultMaxAllowedDelay` = 65s), `Do()` returns the error immediately without sleeping or retrying.
6. Otherwise, `Do()` sleeps for `r.Error.RetryDelay` and retries.


#### Automatic rate limiting support

`r.ComputeRateLimitDelay` defaults to `ComputeDefaultRateLimitDelay`, which treats a response as rate-limited when either:

- status is HTTP 429, or
- `RateLimit-Remaining` / `X-Ratelimit-Remaining` is present and equals `0`.

`ComputeDefaultRateLimitDelay` uses `Retry-After` or `X-RateLimit-Reset*` headers when available, and returns `r.RateLimitFallbackDelay` (default `httpcall.DefaultRateLimitFallbackDelay` = 1m) for 429 requests with no usable reset information.

You can override all of that logic by replacing `r.ComputeRateLimitDelay` with your own function.

After a delay is computed:

- it is used by automatic retries logic by defaulting `r.Error.RetryDelay` to `r.RateLimitDelay`
- you can read `r.Error.RetryDelay` in the returned error response to throttle failures
- you can read `r.RateLimitDelay` to throttle after successful calls


#### Example: exponential backoff

You can implement your own backoff policy using `OnFailed`:

```go
delays := []time.Duration{time.Second, 4 * time.Second, 16 * time.Second, time.Minute}

r.OnFailed(func(r *httpcall.Request) {
	next := delays[min(r.Attempts-1, len(delays)-1)]
	r.Error.RetryDelay = max(next, r.RateLimitDelay)
})
```

#### Rate limiting adjustments

- Disable rate-limit delay computation: `r.ComputeRateLimitDelay = httpcall.NoRateLimitDelay`.
- Replace it completely: set `r.ComputeRateLimitDelay` to your own function.
- Tune rate-limit parsing: `MinRateLimitDelay`, `RateLimitExtraBuffer` (a clock skew buffer added to server-reported time), `RateLimitFallbackDelay` (a delay to use when server doesn't provide a usable one), `MaxAllowedDelay`.


### Debugging with `curl`

`r.Curl()` formats the request as a runnable curl command. You can log this to make debugging easier.


### Response size limit

Response body is limited to `MaxResponseLength` bytes (defaults to `httpcall.DefaultMaxResponseLength`). When exceeded, `Do()` fails with `httpcall.ErrResponseTooLong` (wrapped in an `*httpcall.Error`, of course).


## License

Copyright 2023–2025, Andrey Tarantsov. Published under the terms of the [MIT license](LICENSE).
