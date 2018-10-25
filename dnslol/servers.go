package dnslol

import (
	"errors"
	"sync"
)

var (
	addressRequiredErr = errors.New(
		"One or more DNS recursive resolver addresses must be provided")
)

// DNSServerSelectors implement a way to PickServers to send queries to.
type DNSServerSelector interface {
	PickServers() []string
}

// dnsServerConfig holds addresses of DNS recursive resolver servers that can be
// queried.
type dnsServerConfig struct {
	addresses []string
}

// comparisonSelector is a DNSServerSelector that picks all DNS server addresses
// each time so that queries can be compared between them. Concurrent-safe (no
// updates are performed).
type comparisonSelector struct {
	dnsServerConfig
}

// Pick all of the server addresses so that queries can be compared between
// them.
func (w *comparisonSelector) PickServers() []string {
	return w.addresses
}

// NewComparisonSelector creates a DNSServerSelector that returns all of the
// given addresses for every call to PickServers, allowing comparison between
// the full set of addresses. It is concurrent-safe.
func NewComparisonSelector(addresses []string) (DNSServerSelector, error) {
	if len(addresses) < 1 {
		return nil, addressRequiredErr
	}
	return &comparisonSelector{
		dnsServerConfig: dnsServerConfig{
			addresses: addresses,
		},
	}, nil
}

// roundRobinSelector is a DNSServerSelector that picks from the DNS server
// addresses in a round-robin fashion. Concurrent-safe.
type roundRobinSelector struct {
	dnsServerConfig
	whichServer int
	whichMu     sync.Mutex
}

// Pick a single server address, round-robin style. Concurrent-safe.
func (w *roundRobinSelector) PickServers() []string {
	w.whichMu.Lock()
	which := w.whichServer
	w.whichServer++
	w.whichMu.Unlock()
	return []string{w.addresses[which%len(w.addresses)]}
}

// NewRoundRobinSelector creates a DNSServerSelector that returns one DNS server
// address from addresses for every call to PickServers in a round-robin
// fashion. It is concurrent-safe.
func NewRoundRobinSelector(addresses []string) (DNSServerSelector, error) {
	if len(addresses) < 1 {
		return nil, addressRequiredErr
	}
	return &roundRobinSelector{
		dnsServerConfig: dnsServerConfig{
			addresses: addresses,
		},
	}, nil
}
