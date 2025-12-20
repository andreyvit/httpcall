package httpcall

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrorCategory is an application-defined classification that can be attached
// to an Error.
//
// Categories are intended for business-level routing/handling ("unsupported
// event", "invalid credentials", "rate limited"), rather than transport-level
// classification.
//
// A category is comparable by identity (pointer): you typically define package
// globals and check them with errors.Is / Error.IsInCategory.
type ErrorCategory struct {
	// Name is an arbitrary identifier for the category.
	//
	// Categories are intended for business-level classification (as opposed to
	// transport-level classification like "network error" or "HTTP 500").
	Name string
}

// Error returns the category name (so categories can be treated as errors).
func (err *ErrorCategory) Error() string {
	return err.Name
}

// Error represents a failed Request.
//
// Do() returns *Error. The underlying cause is available in Cause and via
// errors.Unwrap / errors.Is / errors.As.
//
// The intent is to make logging/observability easy: Error stores the request
// identity, status code (if any), the raw response body, and flags used by
// retry logic.
type Error struct {
	// CallID is copied from Request.CallID.
	CallID string

	// IsNetwork is true for errors that occurred before a valid HTTP response was
	// received (DNS, timeouts, connection resets, etc), and for some parsing
	// failures that heuristically look like "we didn't get the expected API
	// response at all".
	IsNetwork bool

	// IsRetriable controls retry behavior in Do().
	// It is set by httpcall (network errors and HTTP 5xx) but can be overridden
	// by hooks.
	IsRetriable bool

	// RetryDelay optionally overrides the delay before the next retry attempt.
	// If zero, Request.RetryDelay / DefaultRetryDelay is used.
	RetryDelay time.Duration

	// StatusCode is the HTTP status code, or 0 if a response was never received.
	StatusCode int

	// Type is an optional machine-friendly classification (often an API error code).
	Type string

	// Path is an optional locator for where an error occurred (for example, a JSON
	// path inside a response).
	Path string

	// Message is an optional human-friendly summary.
	Message string

	// RawResponseBody contains the response body read by httpcall, if any.
	RawResponseBody []byte

	// PrintResponseBody controls whether Error() includes RawResponseBody inline.
	// This is useful for logs, but should be used with care if responses might
	// contain secrets.
	PrintResponseBody bool

	// Cause is the underlying error (network failure, JSON parsing error, a
	// validation error, etc).
	Cause error

	// Category1 and Category2 are optional business-level error categories.
	// Only two are supported to keep the type lightweight.
	Category1 *ErrorCategory
	Category2 *ErrorCategory
}

// Error formats the error with request identity (CallID/status) when available.
//
// If PrintResponseBody is true, Error may include the response body in the
// message. See ShortError for a more compact variant.
func (e *Error) Error() string {
	return e.customError(true)
}

// ShortError formats the error without including request identity (CallID and
// status code).
//
// This is useful in logs where the call identity is already included elsewhere
// (for example, when logging "CallID -> ShortError()").
func (e *Error) ShortError() string {
	return e.customError(false)
}
func (e *Error) customError(withIdentity bool) string {
	var buf strings.Builder
	if withIdentity {
		if e.CallID != "" {
			buf.WriteString(e.CallID)
			buf.WriteString(": ")
		}
		if e.StatusCode != 0 {
			fmt.Fprintf(&buf, "HTTP %d", e.StatusCode)
		}
	}
	if e.IsNetwork {
		buf.WriteString("network: ")
	}
	if e.Type != "" {
		buf.WriteString(": ")
		buf.WriteString(e.Type)
	}
	if e.Category1 != nil {
		buf.WriteString(" <")
		buf.WriteString(e.Category1.Name)
		buf.WriteString(">")
	}
	if e.Category2 != nil {
		buf.WriteString(" <")
		buf.WriteString(e.Category2.Name)
		buf.WriteString(">")
	}
	if e.Message != "" {
		buf.WriteString(": ")
		buf.WriteString(e.Message)
	}
	if e.Path != "" {
		buf.WriteString(" [at ")
		buf.WriteString(e.Path)
		buf.WriteString("]")
	}
	if e.Cause != nil {
		buf.WriteString(": ")
		buf.WriteString(e.Cause.Error())
	}
	if e.PrintResponseBody {
		buf.WriteString("  // response: ")
		if len(e.RawResponseBody) == 0 {
			buf.WriteString("<empty>")
		} else if utf8.Valid(e.RawResponseBody) {
			buf.Write(e.RawResponseBody)
		} else {
			return fmt.Sprintf("<binary %d bytes>", len(e.RawResponseBody))
		}
	}
	return buf.String()
}

// Unwrap returns the underlying cause, enabling errors.Is / errors.As.
func (e *Error) Unwrap() error {
	return e.Cause
}

// IsUnprocessableEntity reports whether StatusCode is HTTP 422.
func (e *Error) IsUnprocessableEntity() bool {
	return e.StatusCode == http.StatusUnprocessableEntity
}

// AddCategory attaches a category to the error and returns e.
//
// At most two categories are supported; adding a third panics.
func (e *Error) AddCategory(cat *ErrorCategory) *Error {
	if cat != nil && !e.IsInCategory(cat) {
		if e.Category1 == nil {
			e.Category1 = cat
		} else if e.Category2 == nil {
			e.Category2 = cat
		} else {
			panic("only 2 categories are supported per error")
		}
	}
	return e
}

// Is implements a custom errors.Is behavior: comparing an Error against an
// *ErrorCategory checks category membership.
func (e *Error) Is(target error) bool {
	if cat, ok := target.(*ErrorCategory); ok {
		return e.IsInCategory(cat)
	}
	return false
}

// IsInCategory reports whether this error has the specified category attached.
func (e *Error) IsInCategory(cat *ErrorCategory) bool {
	return cat != nil && (e.Category1 == cat || e.Category2 == cat)
}
