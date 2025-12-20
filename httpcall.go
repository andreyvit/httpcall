// Package httpcall takes boilerplate out of typical non-streaming single-roundtrip HTTP API calls.
//
// This package is intentionally small and opinionated:
//
//   - One request → one response body (no streaming).
//   - Automatic JSON request/response handling for the common case.
//   - A safety response-size limit (to avoid accidentally downloading huge payloads).
//   - Built-in retry loop (network errors + HTTP 5xx) with pluggable policy.
//
// The key design feature is composable configuration.
//
// Instead of having every API client reinvent retries, logging, auth, rate
// limiting, etc, you build a Request close to the call site and then let the
// "outer environment" configure it by composing hooks with OnStarted/OnFailed/
// OnFinished/OnShouldStart/OnValidate. This lets wrappers add behavior without
// clobbering existing configuration.
package httpcall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultMaxResponseLength is the default maximum number of bytes read from the
// response body.
//
// It exists as a safety net: most API calls should not download huge payloads.
// Override per call via Request.MaxResponseLength.
//
// The current value is 100 MB, but it can be adjusted in later versions. If you
// rely on accepting huge respones, set an explicit value.
const DefaultMaxResponseLength int64 = 100 * 1024 * 1024

// DefaultRetryDelay is the delay between automatic retries when neither
// Error.RetryDelay nor Request.RetryDelay is set.
const DefaultRetryDelay time.Duration = 500 * time.Millisecond

// ErrResponseTooLong is returned (wrapped in *Error) when reading the response
// body exceeds Request.MaxResponseLength / DefaultMaxResponseLength.
var ErrResponseTooLong = errors.New("response too long")

// Request describes a single HTTP API call.
//
// Typical usage:
//
//	r := &httpcall.Request{
//		CallID:      "ListWidgets",
//		Method:      http.MethodGet,
//		BaseURL:     "https://api.example.com",
//		Path:        "/v1/widgets",
//		OutputPtr:   &out,
//		MaxAttempts: 3,
//	}
//	configure(r)
//	err := r.Do()
//
// Init() freezes derived state (it builds HTTPRequest and marshals Input).
// If you want to reuse a request as a template, use Clone() and modify the
// clone before calling Do().
type Request struct {
	// Context is used for request cancellation and for sleeps between retries.
	// If nil, context.Background() is used.
	Context context.Context

	// CallID is an optional human-friendly identifier used in errors/logging.
	CallID string

	// Method is required unless HTTPRequest is provided.
	Method string

	// BaseURL is joined with Path when Path is not an absolute URL. Example:
	// "https://api.example.com".
	BaseURL string

	// Path is either a URL path (when used with BaseURL) or an absolute URL
	// when it contains "://".
	Path string

	// PathParams provides placeholder substitutions in Path. Each key is
	// replaced with the URL-path-escaped value via strings.ReplaceAll.
	//
	// This is intentionally "dumb" (no templating), but extremely effective in
	// practice and helps avoid subtle double-encoding bugs.
	PathParams map[string]string

	// QueryParams are encoded and appended to the final URL.
	QueryParams url.Values

	// FullURLOverride is for APIs using REST-style links.
	//
	// If it contains "://", it is treated as an absolute URL and overrides
	// BaseURL, Path and QueryParams entirely.
	//
	// Otherwise it must start with "/" and replaces only the URL path.
	FullURLOverride string

	// Input is marshaled into the request body unless RawRequestBody is
	// provided.
	//
	// If Input is url.Values, it is form-encoded. Otherwise it is
	// JSON-marshaled.
	Input any

	// RawRequestBody, when non-nil, is used as-is. When nil and Input is
	// provided, it is computed from Input.
	RawRequestBody []byte

	// RequestBodyContentType is applied as the "Content-Type" header.
	//
	// When Input is marshaled automatically, it defaults to JSON or
	// form-encoding content types.
	RequestBodyContentType string

	// Headers are applied to the request. They are copied into HTTPRequest and
	// then r.Headers is set to the final request headers map.
	Headers http.Header

	// BasicAuth sets the HTTP Basic Authorization header (unless overwritten by
	// Headers).
	BasicAuth BasicAuth

	// HTTPRequest can be provided to bypass URL/body construction. If provided,
	// Method and Path are derived from it unless explicitly set.
	HTTPRequest *http.Request

	// OutputPtr controls response parsing:
	//
	//   - nil: body is read but ignored on 2xx
	//   - *[]byte: raw response body is copied as-is
	//   - any other pointer: body is unmarshaled as JSON
	OutputPtr any

	// MaxResponseLength limits response body size for this request. If 0,
	// DefaultMaxResponseLength is used. If negative, response size is
	// unlimited.
	MaxResponseLength int64

	// HTTPClient is used to perform the request. If nil, http.DefaultClient is
	// used.
	HTTPClient *http.Client

	// MaxAttempts controls retry behavior. 1 or 0 means "no retries".
	MaxAttempts int

	// RetryDelay is used between retries when Error.RetryDelay is not set. If
	// 0, DefaultRetryDelay is used.
	RetryDelay time.Duration

	// LastRetryDelay records the most recent delay actually used between
	// attempts.
	LastRetryDelay time.Duration

	// ShouldStart is called right before each attempt. Returning an error
	// aborts the call and Do returns an *Error wrapping it.
	ShouldStart func(r *Request) error

	// Started is called at the beginning of each attempt, after ShouldStart.
	Started func(r *Request)

	// ValidateOutput runs after a successful (2xx) response is parsed. If it
	// returns an error, the request fails with IsRetriable=false.
	ValidateOutput func() error

	// ParseResponse overrides response parsing for 2xx responses. It can
	// populate OutputPtr (or ignore it) as it likes.
	ParseResponse func(r *Request) error

	// ParseErrorResponse can inspect/transform Error for non-2xx responses. It
	// may set r.Error fields, or even set r.Error=nil to suppress the error.
	ParseErrorResponse func(r *Request)

	// Failed is called after an attempt fails (r.Error != nil), before retry
	// logic.
	Failed func(r *Request)

	// Finished is called once after all attempts (success or final failure).
	Finished func(r *Request)

	// UserObject and UserData are reserved for callers/frameworks to attach
	// arbitrary data for logging/metrics/correlation, etc.
	UserObject any
	UserData   map[string]any

	// Attempts is the number of attempts performed by the most recent Do().
	Attempts int

	// HTTPResponse is set after the HTTP round-trip succeeds.
	HTTPResponse *http.Response

	// RawResponseBody is the raw body read from HTTPResponse.Body (bounded by
	// MaxResponseLength).
	RawResponseBody []byte

	// Error is the last error produced by Do(), if any.
	Error *Error

	// Duration measures the time spent inside a single attempt.
	Duration time.Duration

	initDone bool
}

// BasicAuth holds credentials for HTTP Basic authentication.
type BasicAuth struct {
	// Username is the HTTP Basic username.
	Username string

	// Password is the HTTP Basic password.
	Password string
}

// Clone makes a shallow copy of the request and clones Headers/QueryParams
// maps.
//
// This is primarily meant for "template request" patterns where some parts are
// shared and then specialized per call.
func (r *Request) Clone() *Request {
	result := new(Request)
	*result = *r
	if result.QueryParams != nil {
		result.QueryParams = maps.Clone(result.QueryParams)
	}
	if result.Headers != nil {
		result.Headers = maps.Clone(result.Headers)
	}
	return result
}

// IsIdempotent reports whether the HTTP method is considered idempotent by this
// package (GET or HEAD).
//
// This could be used by outer layers to disallow mutations in read-only mode.
func (r *Request) IsIdempotent() bool {
	r.Init()
	return r.HTTPRequest.Method == http.MethodGet || r.HTTPRequest.Method == http.MethodHead
}

// StatusCode returns the HTTP status code from the last response, or 0 if the
// request hasn't received a response yet (e.g. network error before headers).
func (r *Request) StatusCode() int {
	if r.HTTPResponse == nil {
		return 0
	}
	return r.HTTPResponse.StatusCode
}

// SetHeader sets a header in r.Headers, creating the header map if needed.
//
// Prefer this helper when building requests so callers don't need to remember
// to initialize Headers.
func (r *Request) SetHeader(key, value string) {
	if r.Headers == nil {
		r.Headers = make(http.Header)
	}
	r.Headers.Set(key, value)
}

// OnShouldStart composes a new ShouldStart hook on top of the existing one.
//
// Order: The previous hook runs first; if it returns an error, the new hook is
// not run.
func (r *Request) OnShouldStart(f func(r *Request) error) {
	if f == nil {
		return
	}
	if prev := r.ShouldStart; prev != nil {
		r.ShouldStart = func(r *Request) error {
			if err := prev(r); err != nil {
				return err
			}
			return f(r)
		}
	} else {
		r.ShouldStart = f
	}
}

// OnStarted composes a new Started hook on top of the existing one.
//
// Order: The previous hook runs first, then the new hook.
func (r *Request) OnStarted(f func(r *Request)) {
	if f == nil {
		return
	}
	if prev := r.Started; prev != nil {
		r.Started = func(r *Request) {
			prev(r)
			f(r)
		}
	} else {
		r.Started = f
	}
}

// OnFailed composes a new Failed hook on top of the existing one.
//
// Order: The new hook runs first, then the previous hook.
//
// This order is useful when the "inner" call site wants to run logic before a
// broader framework handler runs (for example, to modify Error.IsRetriable or
// to wrap the error cause).
func (r *Request) OnFailed(f func(r *Request)) {
	if f == nil {
		return
	}
	if prev := r.Failed; prev != nil {
		r.Failed = func(r *Request) {
			f(r)
			prev(r)
		}
	} else {
		r.Failed = f
	}
}

// OnFinished composes a new Finished hook on top of the existing one.
//
// Order: The new hook runs first, then the previous hook.
func (r *Request) OnFinished(f func(r *Request)) {
	if f == nil {
		return
	}
	if prev := r.Finished; prev != nil {
		r.Finished = func(r *Request) {
			f(r)
			prev(r)
		}
	} else {
		r.Finished = f
	}
}

// OnValidate composes a new ValidateOutput hook on top of the existing one.
//
// Order: The previous hook runs first; if it returns an error, the new hook is
// not run.
func (r *Request) OnValidate(f func() error) {
	if f == nil {
		return
	}
	if prev := r.ValidateOutput; prev != nil {
		r.ValidateOutput = func() error {
			if err := prev(); err != nil {
				return err
			}
			return f()
		}
	} else {
		r.ValidateOutput = f
	}
}

// Init prepares the request for execution.
//
// It is automatically called by Do(), Curl(), and IsIdempotent().
//
// Init is idempotent: subsequent calls are no-ops. That also means that if you
// modify request fields after Init has run, the changes will not be reflected
// in the prepared HTTPRequest or request body.
//
// Panics:
//
//   - if neither HTTPRequest nor Method is set
//   - if neither HTTPRequest nor (BaseURL/Path) is set
//   - if Method is GET/HEAD and a body is specified
//   - if FullURLOverride is a relative path not starting with "/"
func (r *Request) Init() {
	if r.initDone {
		return
	}
	r.initDone = true

	if r.Context == nil {
		r.Context = context.Background()
	}
	if r.HTTPRequest == nil {
		for k, v := range r.PathParams {
			r.Path = strings.ReplaceAll(r.Path, k, url.PathEscape(v))
		}

		var urlStr string
		if r.FullURLOverride != "" && strings.Contains(r.FullURLOverride, "://") {
			urlStr = r.FullURLOverride
		} else {
			if r.BaseURL == "" && r.Path == "" {
				panic("BaseURL and/or Path must be specified (or HTTPRequest)")
			}
			u := buildURL(r.BaseURL, r.Path, r.QueryParams)
			if r.FullURLOverride != "" {
				if strings.HasPrefix(r.FullURLOverride, "/") {
					u.Path = r.FullURLOverride
				} else {
					panic(fmt.Errorf("FullURLOverride is not absolute and does not start with a slash: %q", r.FullURLOverride))
				}
			}
			urlStr = u.String()
		}

		if r.Method == "" {
			panic("Method must be specified (or HTTPRequest)")
		}

		if r.Input != nil || r.RawRequestBody != nil {
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				panic("GET incompatible with body (Input / RawRequestBody)")
			}

			if r.RawRequestBody == nil {
				if v, ok := r.Input.(url.Values); ok {
					r.RawRequestBody = []byte(v.Encode())
					if r.RequestBodyContentType == "" {
						r.RequestBodyContentType = "application/x-www-form-urlencoded; charset=utf-8"
					}
				} else {
					r.RawRequestBody = must(json.Marshal(r.Input))
					if r.RequestBodyContentType == "" {
						r.RequestBodyContentType = "application/json"
					}
				}
			}

			r.HTTPRequest = must(http.NewRequestWithContext(r.Context, r.Method, urlStr, bytes.NewReader(r.RawRequestBody)))
			if r.RequestBodyContentType != "" {
				r.HTTPRequest.Header.Set("Content-Type", r.RequestBodyContentType)
			}
		} else {
			r.HTTPRequest = must(http.NewRequestWithContext(r.Context, r.Method, urlStr, nil))

			// We allow setting hr.RequestBodyContentType even when it's not
			// necessary, so that it's easy to provide a default value without
			// checking the method.
		}
	} else {
		if r.Method == "" {
			r.Method = r.HTTPRequest.Method
		}
		if r.Path == "" {
			r.Path = r.HTTPRequest.URL.Path
		}
	}

	if r.BasicAuth != (BasicAuth{}) {
		r.HTTPRequest.SetBasicAuth(r.BasicAuth.Username, r.BasicAuth.Password)
	}

	for k, vv := range r.Headers {
		r.HTTPRequest.Header[k] = vv
	}
	r.Headers = r.HTTPRequest.Header
}

// Do executes the HTTP request with optional retries.
//
// Retries happen only when Error.IsRetriable is true and Attempts <
// MaxAttempts. By default, we retry 5xx responses and network errors. The sleep
// between attempts is taken from Error.RetryDelay, then Request.RetryDelay,
// then DefaultRetryDelay.
//
// The returned error, if any, is always *Error.
func (r *Request) Do() error {
	r.Init()

	r.Attempts = 0 // reset in case we're doing same request twice
	for {
		r.Attempts++

		if r.ShouldStart != nil {
			if err := r.ShouldStart(r); err != nil {
				return &Error{
					CallID: r.CallID,
					Cause:  err,
				}
			}
		}

		if r.Started != nil {
			r.Started(r)
		}

		start := time.Now()
		r.Error = r.doOnce()
		r.Duration = time.Since(start)

		if r.Error != nil && r.Failed != nil {
			r.Failed(r)
		}
		if r.Error == nil || !r.Error.IsRetriable || r.Attempts >= r.MaxAttempts {
			break
		}
		delay := r.Error.RetryDelay
		if delay == 0 {
			delay = r.RetryDelay
		}
		if delay == 0 {
			delay = DefaultRetryDelay
		}
		r.LastRetryDelay = delay
		sleep(r.Context, delay)
	}

	if r.Finished != nil {
		r.Finished(r)
	}

	if r.Error == nil {
		// avoid returning non-nil error interface with nil *Error inside
		return nil
	}
	return r.Error
}

func (r *Request) doOnce() *Error {
	client := r.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	maxLen := r.MaxResponseLength
	if maxLen == 0 {
		maxLen = DefaultMaxResponseLength
	}

	resp, err := client.Do(r.HTTPRequest)
	if err != nil {
		return &Error{
			CallID:      r.CallID,
			IsNetwork:   true,
			IsRetriable: true,
			Cause:       err,
		}
	}
	defer resp.Body.Close()
	r.HTTPResponse = resp

	if maxLen > 0 {
		r.RawResponseBody, err = io.ReadAll(&limitedReader{resp.Body, maxLen})
	} else {
		r.RawResponseBody, err = io.ReadAll(resp.Body)
	}
	if err != nil {
		isNetwork := !errors.Is(err, ErrResponseTooLong)
		return &Error{
			CallID:      r.CallID,
			IsNetwork:   isNetwork,
			IsRetriable: isNetwork,
			Cause:       err,
		}
	}

	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		var err error
		var isNetwork bool
		if r.ParseResponse != nil {
			err = r.ParseResponse(r)
		} else if r.OutputPtr == nil {
			// ignore body
		} else if bytePtr, ok := r.OutputPtr.(*[]byte); ok {
			*bytePtr = r.RawResponseBody
		} else {
			err = json.Unmarshal(r.RawResponseBody, r.OutputPtr)
			if err != nil {
				isNetwork = (len(r.RawResponseBody) == 0 || (r.RawResponseBody[0] != '{' && r.RawResponseBody[0] != '['))
			}
		}
		if err != nil {
			return &Error{
				CallID:            r.CallID,
				IsNetwork:         isNetwork,
				IsRetriable:       isNetwork,
				StatusCode:        resp.StatusCode,
				Message:           "error parsing response",
				RawResponseBody:   r.RawResponseBody,
				PrintResponseBody: true,
				Cause:             err,
			}
		}
		if r.ValidateOutput != nil {
			err := r.ValidateOutput()
			if err != nil {
				return &Error{
					CallID:            r.CallID,
					IsNetwork:         false,
					IsRetriable:       false,
					StatusCode:        resp.StatusCode,
					RawResponseBody:   r.RawResponseBody,
					PrintResponseBody: true,
					Cause:             err,
				}
			}
		}
		return nil
	} else {
		r.Error = &Error{
			CallID:            r.CallID,
			StatusCode:        resp.StatusCode,
			IsRetriable:       (resp.StatusCode >= 500 && resp.StatusCode <= 599),
			RawResponseBody:   r.RawResponseBody,
			PrintResponseBody: true,
		}
		if r.ParseErrorResponse != nil {
			r.ParseErrorResponse(r)
			if r.Error == nil {
				return nil // avoid returning non-nil error interface with nil *Error inside
			}
		}
		return r.Error
	}
}

func buildURL(baseURL, path string, queryParams url.Values) *url.URL {
	var u *url.URL
	if strings.Contains(path, "://") || baseURL == "" {
		u = must(url.Parse(path))
	} else {
		u = must(url.Parse(baseURL))
		u.Path = joinURLPath(u.Path, path)
	}
	if len(queryParams) > 0 {
		u.RawQuery = queryParams.Encode()
	}
	return u
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func joinURLPath(base, url string) string {
	if base == "" {
		return url
	}
	if url == "" {
		return base
	}
	return strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(url, "/")
}

func sleep(ctx context.Context, dur time.Duration) {
	ctxDone := ctx.Done()
	if ctxDone == nil {
		time.Sleep(dur)
	} else {
		timer := time.NewTimer(dur)
		select {
		case <-timer.C:
			break
		case <-ctx.Done():
			timer.Stop()
		}
	}
}

// Like io.LimitedReader, but returns ErrResponseTooLong instead of EOF.
type limitedReader struct {
	R io.Reader // underlying reader
	N int64     // max bytes remaining
}

func (l *limitedReader) Read(p []byte) (n int, err error) {
	if l.N <= 0 {
		return 0, ErrResponseTooLong
	}
	if int64(len(p)) > l.N {
		p = p[0:l.N]
	}
	n, err = l.R.Read(p)
	l.N -= int64(n)
	return
}
