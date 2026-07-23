package safehttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

// AllowPrivateFetchesEnv exists solely so Go tests can use httptest servers.
// Production binaries ignore it; user-controlled fetches never get a global
// private-network bypass.
const AllowPrivateFetchesEnv = "SCRIBE_TEST_ALLOW_PRIVATE_FETCHES"

var blockedPrefixes = mustParsePrefixes(
	"0.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	// Non-public special-purpose ranges are rejected explicitly even when the
	// host kernel reports them as global unicast. Some enterprise networks
	// route documentation or benchmarking space internally, so allowing these
	// ranges would turn that routing choice into an SSRF bypass.
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.88.99.0/24",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"240.0.0.0/4",
	"::/128",
	"::1/128",
	// Reject translation/tunnelling prefixes that can encode an otherwise
	// blocked IPv4 destination, plus IPv6 discard/documentation space.
	"64:ff9b::/96",
	"64:ff9b:1::/48",
	"100::/64",
	"2001::/23",
	"2001:db8::/32",
	"2002::/16",
	"fe80::/10",
)

var client = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext:           safeDialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if err := ValidateURL(req.URL); err != nil {
			return err
		}
		return nil
	},
}

var noRedirectClient = &http.Client{
	Timeout:   client.Timeout,
	Transport: client.Transport,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("redirects are not allowed")
	},
}

func Get(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, errors.New("build HTTP request: invalid URL")
	}
	return Do(req)
}

func Do(req *http.Request) (*http.Response, error) {
	return do(req, client)
}

// DoNoRedirect validates and sends an outbound request without replaying its
// method, headers, or body to a redirect target. Use it for credential- or
// data-bearing POST requests whose configured URL is the complete trust scope.
func DoNoRedirect(req *http.Request) (*http.Response, error) {
	return do(req, noRedirectClient)
}

func do(req *http.Request, httpClient *http.Client) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, errors.New("request URL is required")
	}
	if err := ValidateURL(req.URL); err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req) // #nosec G704 -- request URL is validated here and every dial rejects private address space.
	if err != nil {
		return resp, sanitizeTransportError(err)
	}
	return resp, nil
}

// transportError deliberately omits the request URL and the underlying error
// text. net/http wraps transport failures in url.Error, whose Error method
// includes the complete URL and can therefore expose signed query parameters
// when a caller logs a wrapped failure. Unwrap retains errors.Is/errors.As
// behavior for cancellation, deadlines, and network error classification.
type transportError struct {
	cause error
}

func (e *transportError) Error() string {
	switch {
	case errors.Is(e.cause, context.Canceled):
		return "HTTP request canceled"
	case errors.Is(e.cause, context.DeadlineExceeded), e.Timeout():
		return "HTTP request timed out"
	default:
		return "HTTP request failed"
	}
}

func (e *transportError) Unwrap() error {
	return e.cause
}

func (e *transportError) Timeout() bool {
	var networkError net.Error
	return errors.As(e.cause, &networkError) && networkError.Timeout()
}

func sanitizeTransportError(err error) error {
	for {
		var urlError *url.Error
		if !errors.As(err, &urlError) || urlError.Err == nil || urlError.Err == err {
			break
		}
		err = urlError.Err
	}
	return &transportError{cause: err}
}

// ValidateURL validates a URL before a network request. Tests may explicitly
// enable private fetches so integration fixtures can use httptest servers.
func ValidateURL(u *url.URL) error {
	return validateURL(u, privateFetchesAllowed())
}

// ValidatePublicURL validates an externally supplied URL that will be stored
// as a public resource identity. Unlike ValidateURL, this admission check can
// never be relaxed by the test-only private-fetch flag.
func ValidatePublicURL(u *url.URL) error {
	return validateURL(u, false)
}

func validateURL(u *url.URL, allowPrivate bool) error {
	if u == nil {
		return errors.New("url is required")
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	if u.User != nil {
		return errors.New("url credentials are not allowed")
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return errors.New("url host is required")
	}
	if isLocalHostname(host) && !allowPrivate {
		return fmt.Errorf("local hostname %q is not allowed", host)
	}
	if ip, err := netip.ParseAddr(host); err == nil && !allowPrivate && !addressAllowed(ip) {
		return fmt.Errorf("private address %s is not allowed", ip)
	}
	return nil
}

func ReadAllLimit(r io.Reader, maxBytes int64) ([]byte, error) {
	if r == nil {
		return nil, errors.New("response body is required")
	}
	if maxBytes <= 0 {
		return nil, errors.New("maxBytes must be positive")
	}
	b, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > maxBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes", maxBytes)
	}
	return b, nil
}

func safeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if privateFetchesAllowed() {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(host, port))
	}
	if isLocalHostname(host) {
		return nil, fmt.Errorf("local hostname %q is not allowed", host)
	}
	resolved, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("host %q did not resolve", host)
	}
	for _, ip := range resolved {
		if !addressAllowed(ip) {
			return nil, fmt.Errorf("host %q resolved to private address %s", host, ip)
		}
	}
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	remoteAddr, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("remote address for host %q is not TCP", host)
	}
	remoteIP, ok := netip.AddrFromSlice(remoteAddr.IP)
	if !ok || !addressAllowed(remoteIP) {
		_ = conn.Close()
		return nil, fmt.Errorf("host %q connected to private address %s", host, remoteAddr.IP.String())
	}
	return conn, nil
}

func addressAllowed(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsMulticast() {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func isLocalHostname(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), ".")
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

func privateFetchesAllowed() bool {
	if !strings.HasSuffix(os.Args[0], ".test") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(AllowPrivateFetchesEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func mustParsePrefixes(raw ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(raw))
	for _, value := range raw {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			panic(err)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}
