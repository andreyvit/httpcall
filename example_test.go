package httpcall_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/andreyvit/httpcall"
)

func ExampleRequest() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answer":42}`))
	}))
	defer srv.Close()

	var resp struct {
		Answer int `json:"answer"`
	}

	err := (&httpcall.Request{
		Context:     context.Background(),
		CallID:      "Answer",
		Method:      http.MethodGet,
		Path:        srv.URL,
		OutputPtr:   &resp,
		MaxAttempts: 1,
	}).Do()

	fmt.Println(err == nil, resp.Answer)
	// Output: true 42
}

func ExampleRequest_composableConfiguration() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var log []string
	var resp struct {
		OK bool `json:"ok"`
	}

	r := &httpcall.Request{
		CallID:      "Composed",
		Method:      http.MethodGet,
		Path:        srv.URL,
		OutputPtr:   &resp,
		MaxAttempts: 1,
	}
	r.OnStarted(func(r *httpcall.Request) { log = append(log, "started-1") })
	r.OnStarted(func(r *httpcall.Request) { log = append(log, "started-2") })
	r.OnFinished(func(r *httpcall.Request) { log = append(log, "finished") })

	_ = r.Do()

	fmt.Println(resp.OK)
	fmt.Println(strings.Join(log, ","))
	// Output:
	// true
	// started-1,started-2,finished
}
