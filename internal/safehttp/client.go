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

const AllowPrivateFetchesEnv = "SCRIBE_ALLOW_PRIVATE_FETCHES"

var blockedPrefixes = mustParsePrefixes(
	"0.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"::/128",
	"::1/128",
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

func Get(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	return Do(req)
}

func Do(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, errors.New("request URL is required")
	}
	if err := ValidateURL(req.URL); err != nil {
		return nil, err
	}
	return client.Do(req) // #nosec G704 -- request URL is validated here and every dial rejects private address space.
}

func ValidateURL(u *url.URL) error {
	if u == nil {
		return errors.New("URL is required")
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	if u.User != nil {
		return errors.New("URL credentials are not allowed")
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return errors.New("URL host is required")
	}
	if isLocalHostname(host) && !privateFetchesAllowed() {
		return fmt.Errorf("local hostname %q is not allowed", host)
	}
	if ip, err := netip.ParseAddr(host); err == nil && !privateFetchesAllowed() && !addressAllowed(ip) {
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
