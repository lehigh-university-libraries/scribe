// Package servicehttp constructs HTTP clients for trusted, server-configured
// internal and provider services.
package servicehttp

import (
	"errors"
	"net/http"
	"time"
)

// ErrRedirectBlocked is returned for every service redirect. Service requests
// can carry bearer credentials, so even same-origin redirects are not followed
// implicitly; operators must configure the final exact endpoint.
var ErrRedirectBlocked = errors.New("service redirects are not allowed")

// NewClient returns a bounded client that never forwards a request, body, or
// authorization header to a redirect target.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrRedirectBlocked
		},
	}
}
