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

Retries:

- Set `MaxAttempts` to enable retries (`1` means “no retries”).
- By default, retries happen on network errors and HTTP 5xx.
- In `Failed` / `OnFailed`, you can override `r.Error.IsRetriable` and `r.Error.RetryDelay`.

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
