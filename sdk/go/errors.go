package hansho

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// APIError is a non-2xx answer from the hub. API keys never appear in it.
type APIError struct {
	Method string
	Path   string
	// Status is the HTTP status code.
	Status int
	// Code is the hub's error code (the "error" member), for example
	// "invalid_request" or "unknown_channel". Empty when the body was not JSON.
	Code string
	// Field names the offending request field for validation failures.
	Field string
	// Message is the hub's human-readable validation message, when present.
	Message string
	// RetryAfter is the parsed Retry-After header of a 429 (or 503), or zero.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "hansho: %s %s: %d", e.Method, e.Path, e.Status)
	if e.Code != "" {
		b.WriteString(" " + e.Code)
	}
	if e.Field != "" {
		b.WriteString(" (field " + e.Field + ")")
	}
	if e.RetryAfter > 0 {
		fmt.Fprintf(&b, " (retry after %s)", e.RetryAfter)
	}
	return b.String()
}

// NetworkError means no usable HTTP answer arrived: the connection failed, the
// context ended or the response could not be read. It is distinct from
// *APIError, which means the hub answered.
type NetworkError struct {
	Method string
	Path   string
	Err    error
}

func (e *NetworkError) Error() string {
	return fmt.Sprintf("hansho: %s %s: %v", e.Method, e.Path, e.Err)
}

func (e *NetworkError) Unwrap() error { return e.Err }

// IsAPIError returns the *APIError in err's chain, if any.
func IsAPIError(err error) (*APIError, bool) {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}

// ErrEmptyID is returned when a required identifier is empty.
var ErrEmptyID = errors.New("hansho: id must not be empty")

// ErrSoundTooLarge is returned by PutSound for audio over the hub's 1 MB limit.
var ErrSoundTooLarge = errors.New("hansho: sound exceeds 1 MB")
