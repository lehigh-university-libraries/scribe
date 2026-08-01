package proxytrust

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
)

type lookupFixture struct {
	mu        sync.Mutex
	addresses map[string][]net.IPAddr
	calls     int
	wait      <-chan struct{}
	err       error
}

func (f *lookupFixture) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	f.mu.Lock()
	f.calls++
	addresses := append([]net.IPAddr(nil), f.addresses[host]...)
	wait := f.wait
	err := f.err
	f.mu.Unlock()
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return addresses, err
}

func TestTrustedPeerRefreshIgnoresRequestCancellationAndCoalescesLookups(t *testing.T) {
	release := make(chan struct{})
	lookup := &lookupFixture{
		addresses: map[string][]net.IPAddr{"traefik": {{IP: net.ParseIP("172.22.0.7")}}},
		wait:      release,
	}
	resolver := newResolver(lookup, time.Minute, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan bool, 1)
	go func() {
		first <- resolver.TrustedPeer(ctx, "172.22.0.7", nil, config.HostList{"traefik"})
	}()

	deadline := time.Now().Add(time.Second)
	for {
		lookup.mu.Lock()
		calls := lookup.calls
		lookup.mu.Unlock()
		if calls == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("DNS refresh did not start")
		}
		time.Sleep(time.Millisecond)
	}
	second := make(chan bool, 1)
	go func() {
		second <- resolver.TrustedPeer(context.Background(), "172.22.0.7", nil, config.HostList{"traefik"})
	}()
	cancel()
	close(release)
	if !<-first || !<-second {
		t.Fatal("request cancellation poisoned the shared trusted-host refresh")
	}
	lookup.mu.Lock()
	defer lookup.mu.Unlock()
	if lookup.calls != 1 {
		t.Fatalf("DNS calls = %d, want one coalesced refresh", lookup.calls)
	}
}

func TestTrustedPeerDoesNotCacheLookupErrors(t *testing.T) {
	lookup := &lookupFixture{
		addresses: map[string][]net.IPAddr{"traefik": {{IP: net.ParseIP("172.22.0.7")}}},
		err:       errors.New("temporary DNS error"),
	}
	resolver := newResolver(lookup, time.Minute, time.Second)
	if resolver.TrustedPeer(context.Background(), "172.22.0.7", nil, config.HostList{"traefik"}) {
		t.Fatal("failed DNS lookup established proxy trust")
	}
	lookup.mu.Lock()
	lookup.err = nil
	lookup.mu.Unlock()
	if !resolver.TrustedPeer(context.Background(), "172.22.0.7", nil, config.HostList{"traefik"}) {
		t.Fatal("transient DNS failure was cached")
	}
}

func TestTrustedPeerResolvesOnlyConfiguredDirectPeer(t *testing.T) {
	lookup := &lookupFixture{addresses: map[string][]net.IPAddr{
		"traefik": {{IP: net.ParseIP("172.22.0.7")}},
	}}
	resolver := newResolver(lookup, time.Minute, time.Second)
	hosts := config.HostList{"traefik"}

	if !resolver.TrustedPeer(context.Background(), "172.22.0.7:49152", nil, hosts) {
		t.Fatal("configured Traefik address was not trusted")
	}
	if resolver.TrustedPeer(context.Background(), "172.22.0.8:49152", nil, hosts) {
		t.Fatal("unresolved peer was trusted")
	}
	if !resolver.TrustedPeer(context.Background(), "192.0.2.8:443", config.CIDRList{"192.0.2.8/32"}, nil) {
		t.Fatal("explicit CIDR peer was not trusted")
	}
	if lookup.calls != 1 {
		t.Fatalf("DNS calls = %d, want one cached lookup", lookup.calls)
	}
}

func TestTrustedPeerRefreshesAfterTTL(t *testing.T) {
	lookup := &lookupFixture{addresses: map[string][]net.IPAddr{
		"traefik": {{IP: net.ParseIP("172.22.0.7")}},
	}}
	resolver := newResolver(lookup, time.Second, time.Second)
	now := time.Unix(100, 0)
	resolver.now = func() time.Time { return now }

	if !resolver.TrustedPeer(context.Background(), "172.22.0.7", nil, config.HostList{"traefik"}) {
		t.Fatal("initial Traefik address was not trusted")
	}
	lookup.addresses["traefik"] = []net.IPAddr{{IP: net.ParseIP("172.22.0.9")}}
	now = now.Add(2 * time.Second)
	if !resolver.TrustedPeer(context.Background(), "172.22.0.9", nil, config.HostList{"traefik"}) {
		t.Fatal("refreshed Traefik address was not trusted")
	}
	if resolver.TrustedPeer(context.Background(), "172.22.0.7", nil, config.HostList{"traefik"}) {
		t.Fatal("expired Traefik address remained trusted")
	}
}
