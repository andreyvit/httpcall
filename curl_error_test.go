package httpcall

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestRequest_Curl(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var out struct {
		OK bool `json:"ok"`
	}
	r := &Request{
		CallID:      "Curl",
		Method:      http.MethodPost,
		Path:        srv.URL + "/v1/x",
		Input:       map[string]any{"a": 1},
		OutputPtr:   &out,
		MaxAttempts: 1,
		Headers: http.Header{
			"X-Test": {"hello world"},
		},
	}
	r.Init()

	want := "curl -i" +
		" -XPOST" +
		" -H " + ShellQuote("Content-Type: application/json") +
		" -H " + ShellQuote("X-Test: hello world") +
		" -d " + ShellQuote(`{"a":1}`) +
		" " + ShellQuote(r.HTTPRequest.URL.String())

	if got := r.Curl(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRequest_Curl_FormInput(t *testing.T) {
	r := &Request{
		Method: http.MethodPost,
		Path:   "https://example.com/x",
		Input: url.Values{
			"a": {"1"},
			"b": {"2"},
		},
	}
	curl := r.Curl()
	if !strings.Contains(curl, " -d a=1") || !strings.Contains(curl, " -d b=2") {
		t.Fatalf("curl=%q", curl)
	}
}

func TestRequest_Curl_RawBodyWhenInputNil(t *testing.T) {
	r := &Request{
		Method:         http.MethodPost,
		Path:           "https://example.com/x",
		Input:          nil,
		RawRequestBody: []byte(`{"x":1}`),
		Headers:        http.Header{"Content-Type": {"application/json"}},
	}
	curl := r.Curl()
	if !strings.Contains(curl, " -d '{\"x\":1}'") {
		t.Fatalf("curl=%q", curl)
	}
}

func TestErrorFormattingAndCategories(t *testing.T) {
	c1 := &ErrorCategory{Name: "cat1"}
	c2 := &ErrorCategory{Name: "cat2"}
	c3 := &ErrorCategory{Name: "cat3"}
	if c1.Error() != "cat1" {
		t.Fatalf("got %q", c1.Error())
	}

	e := (&Error{
		CallID:            "Call",
		IsNetwork:         true,
		StatusCode:        400,
		Type:              "bad",
		Message:           "nope",
		Path:              "/x",
		RawResponseBody:   []byte("hi"),
		PrintResponseBody: true,
		Cause:             errors.New("boom"),
	}).AddCategory(c1).AddCategory(c2).AddCategory(c3)

	if !e.Is(c1) || !e.Is(c2) {
		t.Fatalf("expected categories to match via errors.Is")
	}
	if !e.Is(c3) {
		t.Fatalf("expected 3rd category to match via errors.Is")
	}
	if e.IsUnprocessableEntity() {
		t.Fatalf("unexpected")
	}
	_ = e.Error()
	_ = e.ShortError()

	_ = (&Error{PrintResponseBody: true}).Error()

	e2 := &Error{
		PrintResponseBody: true,
		RawResponseBody:   []byte{0xff, 0xfe},
	}
	_ = e2.Error()
}

func TestError_AddCategorySupportsMany(t *testing.T) {
	cats := make([]*ErrorCategory, 0, 10)
	for i := range 10 {
		cats = append(cats, &ErrorCategory{Name: strconv.Itoa(i)})
	}

	e := &Error{}
	e.AddCategory(nil)
	if e.IsInCategory(nil) {
		t.Fatalf("expected nil category to be false")
	}
	for _, cat := range cats {
		e.AddCategory(cat)
	}
	e.AddCategory(cats[0]) // duplicate should be a no-op
	for _, cat := range cats {
		if !e.IsInCategory(cat) {
			t.Fatalf("expected IsInCategory for %q", cat.Name)
		}
		if !errors.Is(e, cat) {
			t.Fatalf("expected errors.Is for %q", cat.Name)
		}
	}
}

func TestRequest_OnShouldStartComposition(t *testing.T) {
	r := &Request{
		Context:     context.Background(),
		CallID:      "Start",
		Method:      http.MethodGet,
		Path:        "https://example.com",
		MaxAttempts: 1,
		HTTPClient: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("should not run")
		})},
	}

	r.OnShouldStart(func(r *Request) error { return errors.New("a") })
	r.OnShouldStart(func(r *Request) error { return errors.New("b") })

	err := r.Do()
	var hcErr *Error
	if !errors.As(err, &hcErr) || hcErr.CallID != "Start" {
		t.Fatalf("got %#v", err)
	}
	if hcErr.Cause == nil || hcErr.Cause.Error() != "a" {
		t.Fatalf("cause=%v", hcErr.Cause)
	}
}

func TestRequest_SetHeader(t *testing.T) {
	r := &Request{}
	r.SetHeader("X", "1")
	if r.Headers.Get("X") != "1" {
		t.Fatalf("got %q", r.Headers.Get("X"))
	}
}

func TestRequest_FullURLOverride(t *testing.T) {
	r := &Request{
		Method:          http.MethodGet,
		BaseURL:         "https://example.com/a",
		Path:            "/b",
		FullURLOverride: "/c",
	}
	r.Init()
	if got := r.HTTPRequest.URL.String(); got != "https://example.com/c" {
		t.Fatalf("got %q", got)
	}

	r2 := &Request{
		Method:          http.MethodGet,
		Path:            "https://example.com/a",
		FullURLOverride: "https://other.example/z",
	}
	r2.Init()
	if got := r2.HTTPRequest.URL.String(); got != "https://other.example/z" {
		t.Fatalf("got %q", got)
	}
}

func TestRequest_MaxResponseLengthUnlimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 64))
	}))
	defer srv.Close()

	r := &Request{
		Method:            http.MethodGet,
		Path:              srv.URL,
		MaxResponseLength: -1,
		MaxAttempts:       1,
	}
	if err := r.Do(); err != nil {
		t.Fatalf("err=%v", err)
	}
}
