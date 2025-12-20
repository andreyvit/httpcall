package httpcall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestShellQuote(t *testing.T) {
	t.Run("NoSpecials", func(t *testing.T) {
		if got := ShellQuote("abcDEF_123"); got != "abcDEF_123" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("SingleQuotesPreferred", func(t *testing.T) {
		if got := ShellQuote("hello world"); got != "'hello world'" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("DoubleQuotesWithEscapes", func(t *testing.T) {
		got := ShellQuote(`a'b$"c\!`)
		want := `"a'b\$\"c\\\!"`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestJoinURLPath(t *testing.T) {
	if got := joinURLPath("", "/x"); got != "/x" {
		t.Fatalf("got %q", got)
	}
	if got := joinURLPath("/a", ""); got != "/a" {
		t.Fatalf("got %q", got)
	}
	if got := joinURLPath("/a/", "/b"); got != "/a/b" {
		t.Fatalf("got %q", got)
	}
}

func TestBuildURL(t *testing.T) {
	u := buildURL("https://example.com/api", "/v1/x", url.Values{"q": {"1"}})
	if u.String() != "https://example.com/api/v1/x?q=1" {
		t.Fatalf("got %q", u.String())
	}

	u = buildURL("", "https://a.example/x", nil)
	if u.String() != "https://a.example/x" {
		t.Fatalf("got %q", u.String())
	}
}

func TestMustPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic")
		}
	}()
	_ = must(123, errors.New("boom"))
}

func TestLimitedReader(t *testing.T) {
	lr := &limitedReader{R: bytes.NewReader([]byte("0123456789")), N: 5}
	b, err := io.ReadAll(lr)
	if err == nil || !errors.Is(err, ErrResponseTooLong) {
		t.Fatalf("got err=%v", err)
	}
	if string(b) != "01234" {
		t.Fatalf("got %q", string(b))
	}

	_, err = lr.Read(make([]byte, 1))
	if !errors.Is(err, ErrResponseTooLong) {
		t.Fatalf("got err=%v", err)
	}
}

func TestSleep(t *testing.T) {
	sleep(context.Background(), 0)

	ctx, cancel := context.WithCancel(context.Background())
	sleep(ctx, 0) // timer.C path when ctx is not cancelled
	cancel()
	sleep(ctx, 10*time.Millisecond)
}

func TestRequest_InitPanics(t *testing.T) {
	t.Run("MissingMethod", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatalf("expected panic")
			}
		}()
		(&Request{Path: "https://example.com"}).Init()
	})

	t.Run("MissingBaseURLAndPath", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatalf("expected panic")
			}
		}()
		(&Request{Method: http.MethodGet}).Init()
	})

	t.Run("GETWithBody", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatalf("expected panic")
			}
		}()
		(&Request{Method: http.MethodGet, Path: "https://example.com", Input: map[string]any{"x": 1}}).Init()
	})

	t.Run("FullURLOverrideBadRelative", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatalf("expected panic")
			}
		}()
		(&Request{
			Method:          http.MethodGet,
			BaseURL:         "https://example.com",
			Path:            "/x",
			FullURLOverride: "y",
		}).Init()
	})
}

func TestRequest_InitBuildsURLAndHeaders(t *testing.T) {
	r := &Request{
		Method:  http.MethodPost,
		BaseURL: "https://example.com/api/",
		Path:    "/v1/:id",
		PathParams: map[string]string{
			":id": "hello/world",
		},
		QueryParams: url.Values{
			"q": {"1"},
		},
		Input: map[string]any{"x": 1},
		Headers: http.Header{
			"X-Foo": {"bar"},
		},
		BasicAuth: BasicAuth{Username: "u", Password: "p"},
	}
	r.Init()

	if got := r.HTTPRequest.URL.String(); got != "https://example.com/api/v1/hello%252Fworld?q=1" {
		t.Fatalf("got %q", got)
	}
	if got := r.HTTPRequest.Header.Get("X-Foo"); got != "bar" {
		t.Fatalf("got %q", got)
	}
	if got := r.HTTPRequest.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("got %q", got)
	}
	u, p, ok := r.HTTPRequest.BasicAuth()
	if !ok || u != "u" || p != "p" {
		t.Fatalf("basic auth not set")
	}
}

func TestRequest_InitWithHTTPRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "https://example.com/x", nil)
	r := &Request{HTTPRequest: req}
	r.Init()
	if r.Method != http.MethodPut {
		t.Fatalf("got %q", r.Method)
	}
	if r.Path != "/x" {
		t.Fatalf("got %q", r.Path)
	}
}

func TestRequest_Clone(t *testing.T) {
	r := &Request{
		QueryParams: url.Values{"a": {"1"}},
		Headers:     http.Header{"X": {"1"}},
	}
	c := r.Clone()
	c.QueryParams.Set("a", "2")
	c.Headers.Set("X", "2")
	if r.QueryParams.Get("a") != "1" {
		t.Fatalf("mutated original query params")
	}
	if r.Headers.Get("X") != "1" {
		t.Fatalf("mutated original headers")
	}
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRequest_Do_NetworkError(t *testing.T) {
	r := &Request{
		CallID:      "Net",
		Method:      http.MethodGet,
		Path:        "https://example.com",
		MaxAttempts: 1,
		HTTPClient: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("nope")
		})},
	}
	err := r.Do()
	var hcErr *Error
	if !errors.As(err, &hcErr) || !hcErr.IsNetwork || !hcErr.IsRetriable {
		t.Fatalf("got %#v", err)
	}
}

func TestRequest_Do_SuccessParsingModes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"a":1}`))
	}))
	defer srv.Close()

	t.Run("OutputPtrStruct", func(t *testing.T) {
		var out struct {
			A int `json:"a"`
		}
		r := &Request{Method: http.MethodGet, Path: srv.URL, OutputPtr: &out, MaxAttempts: 1}
		if err := r.Do(); err != nil {
			t.Fatalf("err=%v", err)
		}
		if out.A != 1 {
			t.Fatalf("out=%#v", out)
		}
	})

	t.Run("OutputPtrBytes", func(t *testing.T) {
		var out []byte
		r := &Request{Method: http.MethodGet, Path: srv.URL, OutputPtr: &out, MaxAttempts: 1}
		if err := r.Do(); err != nil {
			t.Fatalf("err=%v", err)
		}
		if string(out) != `{"a":1}` {
			t.Fatalf("out=%q", string(out))
		}
	})

	t.Run("ParseResponse", func(t *testing.T) {
		var out any
		r := &Request{
			Method:      http.MethodGet,
			Path:        srv.URL,
			MaxAttempts: 1,
			ParseResponse: func(r *Request) error {
				return json.Unmarshal(r.RawResponseBody, &out)
			},
		}
		if err := r.Do(); err != nil {
			t.Fatalf("err=%v", err)
		}
		m, ok := out.(map[string]any)
		if !ok || int(m["a"].(float64)) != 1 {
			t.Fatalf("out=%T %#v", out, out)
		}
	})
}

func TestRequest_Do_ValidateOutputError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"a":1}`))
	}))
	defer srv.Close()

	var out struct {
		A int `json:"a"`
	}
	r := &Request{
		CallID:      "Validate",
		Method:      http.MethodGet,
		Path:        srv.URL,
		OutputPtr:   &out,
		MaxAttempts: 1,
		ValidateOutput: func() error {
			return errors.New("bad output")
		},
	}
	err := r.Do()
	var hcErr *Error
	if !errors.As(err, &hcErr) || hcErr.IsNetwork || hcErr.IsRetriable {
		t.Fatalf("got %#v", err)
	}
}

func TestRequest_Do_ErrorParsingResponseMarksNetworkForNonJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer srv.Close()

	var out struct {
		A int `json:"a"`
	}
	r := &Request{
		CallID:      "Parse",
		Method:      http.MethodGet,
		Path:        srv.URL,
		OutputPtr:   &out,
		MaxAttempts: 1,
	}
	err := r.Do()
	var hcErr *Error
	if !errors.As(err, &hcErr) || !hcErr.IsNetwork || !hcErr.IsRetriable {
		t.Fatalf("got %#v", err)
	}
}

func TestRequest_Do_ErrResponseTooLong(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer srv.Close()

	r := &Request{
		CallID:            "Len",
		Method:            http.MethodGet,
		Path:              srv.URL,
		MaxResponseLength: 5,
		MaxAttempts:       1,
	}
	err := r.Do()
	if !errors.Is(err, ErrResponseTooLong) {
		t.Fatalf("got %v", err)
	}
	var hcErr *Error
	if !errors.As(err, &hcErr) || hcErr.IsNetwork || hcErr.IsRetriable {
		t.Fatalf("got %#v", err)
	}
}

func TestRequest_Do_Non2xx_ParseErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad"}`))
	}))
	defer srv.Close()

	r := &Request{
		CallID:      "Err",
		Method:      http.MethodGet,
		Path:        srv.URL,
		MaxAttempts: 1,
		ParseErrorResponse: func(r *Request) {
			r.Error.Type = "bad_request"
			r.Error.Message = "bad"
		},
	}
	err := r.Do()
	var hcErr *Error
	if !errors.As(err, &hcErr) || hcErr.Type != "bad_request" || hcErr.Message != "bad" {
		t.Fatalf("got %#v", err)
	}
}

func TestRequest_Do_Non2xx_ParseErrorResponseCanSuppressError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`nope`))
	}))
	defer srv.Close()

	r := &Request{
		Method:      http.MethodGet,
		Path:        srv.URL,
		MaxAttempts: 1,
		ParseErrorResponse: func(r *Request) {
			r.Error = nil
		},
	}
	if err := r.Do(); err != nil {
		t.Fatalf("err=%v", err)
	}
}

func TestRequest_Do_Retries(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"x"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var hooks []string
	var out struct {
		OK bool `json:"ok"`
	}
	r := &Request{
		CallID:      "Retry",
		Method:      http.MethodGet,
		Path:        srv.URL,
		OutputPtr:   &out,
		MaxAttempts: 2,
		RetryDelay:  time.Millisecond,
		ParseErrorResponse: func(r *Request) {
			r.Error.Message = "server"
		},
	}
	r.OnStarted(func(r *Request) { hooks = append(hooks, "started") })
	r.OnFailed(func(r *Request) { hooks = append(hooks, "failed") })
	r.OnFinished(func(r *Request) { hooks = append(hooks, "finished") })

	err := r.Do()
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	if !out.OK {
		t.Fatalf("out=%#v", out)
	}
	if strings.Join(hooks, ",") != "started,failed,started,finished" {
		t.Fatalf("hooks=%q", strings.Join(hooks, ","))
	}
	if r.Attempts != 2 {
		t.Fatalf("attempts=%d", r.Attempts)
	}
}

func TestRequest_IsIdempotent(t *testing.T) {
	r := &Request{Method: http.MethodGet, Path: "https://example.com"}
	if !r.IsIdempotent() {
		t.Fatalf("expected idempotent")
	}
}

func TestRequest_HookCompositionOrder(t *testing.T) {
	r := &Request{}

	var started []string
	r.OnStarted(func(r *Request) { started = append(started, "1") })
	r.OnStarted(func(r *Request) { started = append(started, "2") })
	r.Started(r)
	if strings.Join(started, ",") != "1,2" {
		t.Fatalf("started=%q", strings.Join(started, ","))
	}

	var failed []string
	r.OnFailed(func(r *Request) { failed = append(failed, "1") })
	r.OnFailed(func(r *Request) { failed = append(failed, "2") })
	r.Failed(r)
	if strings.Join(failed, ",") != "2,1" {
		t.Fatalf("failed=%q", strings.Join(failed, ","))
	}

	var finished []string
	r.OnFinished(func(r *Request) { finished = append(finished, "1") })
	r.OnFinished(func(r *Request) { finished = append(finished, "2") })
	r.Finished(r)
	if strings.Join(finished, ",") != "2,1" {
		t.Fatalf("finished=%q", strings.Join(finished, ","))
	}

	var validated []string
	r.OnValidate(func() error { validated = append(validated, "1"); return nil })
	r.OnValidate(func() error { validated = append(validated, "2"); return nil })
	if err := r.ValidateOutput(); err != nil {
		t.Fatalf("err=%v", err)
	}
	if strings.Join(validated, ",") != "1,2" {
		t.Fatalf("validated=%q", strings.Join(validated, ","))
	}
}

func TestRequest_HooksNilIsNoop(t *testing.T) {
	r := &Request{}
	r.OnShouldStart(nil)
	r.OnStarted(nil)
	r.OnFailed(nil)
	r.OnFinished(nil)
	r.OnValidate(nil)
	if r.ShouldStart != nil || r.Started != nil || r.Failed != nil || r.Finished != nil || r.ValidateOutput != nil {
		t.Fatalf("expected nil hooks to remain nil")
	}
}

func TestRequest_OnShouldStart_AllBranches(t *testing.T) {
	var called []string

	r := &Request{}
	r.OnShouldStart(func(r *Request) error { called = append(called, "prev"); return nil })
	r.OnShouldStart(func(r *Request) error { called = append(called, "next"); return errors.New("next") })
	err := r.ShouldStart(r)
	if err == nil || err.Error() != "next" {
		t.Fatalf("err=%v", err)
	}
	if strings.Join(called, ",") != "prev,next" {
		t.Fatalf("called=%q", strings.Join(called, ","))
	}

	called = nil
	r2 := &Request{}
	r2.OnShouldStart(func(r *Request) error { called = append(called, "prev"); return errors.New("prev") })
	r2.OnShouldStart(func(r *Request) error { called = append(called, "next"); return nil })
	err = r2.ShouldStart(r2)
	if err == nil || err.Error() != "prev" {
		t.Fatalf("err=%v", err)
	}
	if strings.Join(called, ",") != "prev" {
		t.Fatalf("called=%q", strings.Join(called, ","))
	}
}

func TestRequest_OnValidate_AllBranches(t *testing.T) {
	var called []string

	r := &Request{}
	r.OnValidate(func() error { called = append(called, "prev"); return nil })
	r.OnValidate(func() error { called = append(called, "next"); return errors.New("next") })
	err := r.ValidateOutput()
	if err == nil || err.Error() != "next" {
		t.Fatalf("err=%v", err)
	}
	if strings.Join(called, ",") != "prev,next" {
		t.Fatalf("called=%q", strings.Join(called, ","))
	}

	called = nil
	r2 := &Request{}
	r2.OnValidate(func() error { called = append(called, "prev"); return errors.New("prev") })
	r2.OnValidate(func() error { called = append(called, "next"); return nil })
	err = r2.ValidateOutput()
	if err == nil || err.Error() != "prev" {
		t.Fatalf("err=%v", err)
	}
	if strings.Join(called, ",") != "prev" {
		t.Fatalf("called=%q", strings.Join(called, ","))
	}
}

func TestRequest_StatusCode(t *testing.T) {
	r := &Request{}
	if r.StatusCode() != 0 {
		t.Fatalf("got %d", r.StatusCode())
	}
	r.HTTPResponse = &http.Response{StatusCode: 204}
	if r.StatusCode() != 204 {
		t.Fatalf("got %d", r.StatusCode())
	}
}

func TestRequest_Do_DefaultRetryDelayAndOverrideRetryDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int
	r := &Request{
		Context:     ctx,
		CallID:      "Delay",
		Method:      http.MethodGet,
		Path:        "https://example.com",
		MaxAttempts: 2,
		HTTPClient: &http.Client{Transport: rtFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			status := http.StatusInternalServerError
			body := `{"error":"x"}`
			if calls == 2 {
				status = http.StatusOK
				body = `{"ok":true}`
			}
			return &http.Response{
				StatusCode: status,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    req,
			}, nil
		})},
		ParseErrorResponse: func(r *Request) {},
	}
	r.Failed = func(r *Request) {
		cancel() // ensures DefaultRetryDelay does not actually sleep
	}

	if err := r.Do(); err != nil {
		t.Fatalf("err=%v", err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	if r.LastRetryDelay != DefaultRetryDelay {
		t.Fatalf("LastRetryDelay=%v", r.LastRetryDelay)
	}

	calls = 0
	r2 := &Request{
		CallID:             "DelayOverride",
		Method:             http.MethodGet,
		Path:               "https://example.com",
		MaxAttempts:        2,
		RetryDelay:         time.Hour,
		HTTPClient:         r.HTTPClient,
		ParseErrorResponse: func(r *Request) {},
	}
	r2.Failed = func(r *Request) {
		r.Error.RetryDelay = time.Nanosecond
	}

	if err := r2.Do(); err != nil {
		t.Fatalf("err=%v", err)
	}
	if r2.LastRetryDelay != time.Nanosecond {
		t.Fatalf("LastRetryDelay=%v", r2.LastRetryDelay)
	}
}
