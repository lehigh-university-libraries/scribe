// Package proxytrust authenticates the immediate reverse-proxy peer before
// callers consume Forwarded or X-Forwarded-* request headers.
package proxytrust

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
)

const (
	defaultCacheTTL      = 5 * time.Second
	defaultLookupTimeout = 100 * time.Millisecond
)

type hostLookup interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type cacheEntry struct {
	expires time.Time
	ips     []net.IP
}

type lookupCall struct {
	done chan struct{}
	ips  []net.IP
}

// Resolver bounds and caches trusted-host lookups. A short TTL follows Docker
// service-IP changes without putting DNS on every application request.
type Resolver struct {
	lookup        hostLookup
	cacheTTL      time.Duration
	lookupTimeout time.Duration
	now           func() time.Time

	mu       sync.Mutex
	entries  map[string]cacheEntry
	inflight map[string]*lookupCall
}

func newResolver(lookup hostLookup, cacheTTL, lookupTimeout time.Duration) *Resolver {
	if lookup == nil {
		lookup = net.DefaultResolver
	}
	if cacheTTL <= 0 {
		cacheTTL = defaultCacheTTL
	}
	if lookupTimeout <= 0 {
		lookupTimeout = defaultLookupTimeout
	}
	return &Resolver{
		lookup:        lookup,
		cacheTTL:      cacheTTL,
		lookupTimeout: lookupTimeout,
		now:           time.Now,
		entries:       make(map[string]cacheEntry),
		inflight:      make(map[string]*lookupCall),
	}
}

var defaultResolver = newResolver(net.DefaultResolver, defaultCacheTTL, defaultLookupTimeout)

// TrustedPeer reports whether address is an explicitly configured proxy CIDR
// or currently resolves from an explicitly configured proxy hostname.
func TrustedPeer(ctx context.Context, address string, cidrs config.CIDRList, hosts config.HostList) bool {
	return defaultResolver.TrustedPeer(ctx, address, cidrs, hosts)
}

// TrustedPeer is the instance form used by focused resolver tests.
func (r *Resolver) TrustedPeer(ctx context.Context, address string, cidrs config.CIDRList, hosts config.HostList) bool {
	if config.AddressInCIDRs(address, cidrs) {
		return true
	}
	peer := addressIP(address)
	if peer == nil {
		return false
	}
	for _, host := range hosts {
		for _, candidate := range r.resolve(ctx, host) {
			if candidate.Equal(peer) {
				return true
			}
		}
	}
	return false
}

func addressIP(address string) net.IP {
	host := strings.TrimSpace(address)
	if splitHost, _, err := net.SplitHostPort(host); err == nil {
		host = splitHost
	}
	return net.ParseIP(strings.Trim(strings.TrimSpace(host), "[]"))
}

func (r *Resolver) resolve(ctx context.Context, host string) []net.IP {
	now := r.now()
	r.mu.Lock()
	entry, ok := r.entries[host]
	if ok && now.Before(entry.expires) {
		result := append([]net.IP(nil), entry.ips...)
		r.mu.Unlock()
		return result
	}
	if call, exists := r.inflight[host]; exists {
		r.mu.Unlock()
		select {
		case <-call.done:
			return append([]net.IP(nil), call.ips...)
		case <-ctx.Done():
			return nil
		}
	}
	call := &lookupCall{done: make(chan struct{})}
	r.inflight[host] = call
	r.mu.Unlock()

	// Proxy identity is process configuration, not request data. A canceled
	// request must not cancel and negatively cache the shared DNS refresh.
	lookupCtx, cancel := context.WithTimeout(context.Background(), r.lookupTimeout)
	addresses, err := r.lookup.LookupIPAddr(lookupCtx, host)
	cancel()
	ips := make([]net.IP, 0, len(addresses))
	if err == nil {
		for _, address := range addresses {
			if address.IP != nil {
				ips = append(ips, append(net.IP(nil), address.IP...))
			}
		}
	}

	r.mu.Lock()
	if err == nil {
		r.entries[host] = cacheEntry{expires: r.now().Add(r.cacheTTL), ips: ips}
	}
	call.ips = append([]net.IP(nil), ips...)
	delete(r.inflight, host)
	close(call.done)
	r.mu.Unlock()
	return append([]net.IP(nil), ips...)
}
