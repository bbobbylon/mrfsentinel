package mrf

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrBlockedAddress reports that a fetch was refused before any bytes left
// this process, because the URL resolved to an address that is not on the
// public internet.
//
// It exists as a sentinel so a caller can tell "we refused to dial this" from
// "we dialed and it failed". Nothing currently branches on it — the worker
// deliberately collapses every fetch failure into one user-facing message,
// see unreachableMessage — but a future admin view that wants to explain a
// blocked URL to its owner should use errors.Is rather than matching on
// message text.
var ErrBlockedAddress = errors.New("mrf: the URL resolved to a non-public address")

// unreachableMessage is the single message shown for every network-level
// fetch failure: connection refused, no such host, timed out, and blocked by
// the address filter all produce this exact string.
//
// The uniformity is the point. A hospital's MRF URL is attacker-controlled
// input (anyone with an account can add a hospital pointing anywhere), and
// internal/web's run page renders ErrorMessage verbatim. Distinguishing those
// four cases would turn the report page into an internal port scanner:
// "connection refused" says nothing is listening, a read timeout says a
// firewall swallowed it, and "blocked" says the address is internal — three
// different answers that together map a private network. The full error still
// reaches the operator through the structured log in
// internal/validation/worker.go, which is where it belongs.
const unreachableMessage = "The MRF URL could not be reached. Check that it is correct, publicly reachable, and not behind a login."

// maxFetchRedirects caps how many hops Fetch will follow. Go's default is 10;
// this is lower because a published MRF that needs more than a couple of
// redirects is misconfigured, and every extra hop is another chance to be
// pointed somewhere new after the first address check passed.
const maxFetchRedirects = 5

// FetchError carries two descriptions of one failure: a Public message safe
// to store in a validation run's error_message column and show the hospital's
// owner, and the wrapped Err with the full detail for the log.
//
// The split exists because those two audiences want opposite things. The
// operator reading logs wants the exact dial error; the user reading the
// report page must not be told which of several internal hosts refused the
// connection. internal/validation/worker.go is the one place that pulls the
// two apart — it logs err and stores err.Public — so any failure path here
// that forgets to return a *FetchError degrades safely to the generic
// message rather than leaking.
type FetchError struct {
	// Public is shown to the user. It must never vary with anything only
	// the target host could tell us about a private network.
	Public string

	// Err is the underlying failure, logged but never displayed.
	Err error
}

// Error makes *FetchError an error, reporting the internal detail — this is
// what reaches the log, not the user.
func (e *FetchError) Error() string { return e.Err.Error() }

// Unwrap exposes the wrapped cause so errors.Is(err, ErrBlockedAddress) and
// friends still work through the wrapper.
func (e *FetchError) Unwrap() error { return e.Err }

// blockedPrefixes covers the non-public ranges netip.Addr has no predicate
// for. The ones it does have — loopback, unspecified, private (which is both
// RFC 1918 and IPv6 unique-local fc00::/7), link-local, and multicast — are
// tested directly in isPublicAddr rather than repeated here.
//
// 169.254.169.254, the cloud instance-metadata endpoint that makes SSRF worth
// exploiting on AWS/GCP/Azure, is link-local and so already excluded; Alibaba
// Cloud's 100.100.100.200 is why CGNAT space is in this list.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network", RFC 1122
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT, RFC 6598
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments, RFC 6890
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1, RFC 5737
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking, RFC 2544
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2, RFC 5737
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3, RFC 5737
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, RFC 1112
	netip.MustParsePrefix("64:ff9b:1::/48"),  // IPv4/IPv6 translation, RFC 8215
	netip.MustParsePrefix("2001:db8::/32"),   // documentation, RFC 3849
}

// isPublicAddr reports whether addr is somewhere a hospital could plausibly
// publish an MRF, as opposed to somewhere only this server can reach.
//
// Unmap runs first and is not cosmetic: ::ffff:127.0.0.1 is a perfectly legal
// way to write loopback that every predicate below would otherwise miss,
// because on the 16-byte form none of them match. Collapsing to the 4-byte
// form makes one check cover both spellings.
func isPublicAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return false
	}
	switch {
	case addr.IsLoopback(),
		addr.IsUnspecified(),
		addr.IsPrivate(),
		addr.IsLinkLocalUnicast(),
		addr.IsLinkLocalMulticast(),
		addr.IsInterfaceLocalMulticast(),
		addr.IsMulticast():
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

// refuseNonPublicAddress is the net.Dialer.Control hook on publicOnlyClient:
// the last point before a TCP connection is opened, called once per
// connection with the address DNS actually produced.
//
// Checking here rather than at the point the user submits the URL is the
// whole design. A submit-time check inspects a hostname, and a hostname is
// not a destination: the name can resolve to a public address when it is
// validated and to 169.254.169.254 a second later (DNS rebinding), or the
// public address can 302 straight to an internal one. Control sees neither
// the hostname nor the redirect chain — only the numeric address about to be
// connected to — so both of those routes come back through it, and there is
// no window between the check and the connect for the answer to change.
//
// See internal/web/handlers_dashboard.go for the scheme check that still
// happens at submit time; that one is about giving the user an immediate
// error message, not about safety.
func refuseNonPublicAddress(network, address string, _ syscall.RawConn) error {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return fmt.Errorf("%w: refusing to dial network %q", ErrBlockedAddress, network)
	}

	addrPort, err := netip.ParseAddrPort(address)
	if err != nil {
		// Control is documented to receive an already-resolved address, so
		// this is unreachable in practice. Failing closed anyway costs
		// nothing and means a future Go change cannot turn the filter off.
		return fmt.Errorf("%w: unparseable dial address %q", ErrBlockedAddress, address)
	}
	if !isPublicAddr(addrPort.Addr()) {
		return fmt.Errorf("%w: %s", ErrBlockedAddress, addrPort.Addr())
	}
	return nil
}

// checkRedirect bounds the redirect chain and re-checks the scheme on every
// hop. It is defence in depth rather than the main control:
// refuseNonPublicAddress already fires on each hop's dial, and net/http
// declines to follow a redirect to a non-http(s) scheme on its own. The value
// here is that a redirect to file:// or gopher:// fails with an error naming
// what happened instead of a generic one.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxFetchRedirects {
		return fmt.Errorf("mrf: stopped after %d redirects", maxFetchRedirects)
	}
	switch req.URL.Scheme {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("%w: redirect to scheme %q", ErrBlockedAddress, req.URL.Scheme)
	}
}

// newFetchClient builds one of the two package-level clients below. Passing
// control as nil produces an ordinary dialer, which is what makes the
// unrestricted client unrestricted.
//
// The transport mirrors http.DefaultTransport's timeouts and pool sizes
// deliberately — this replaced http.DefaultClient, and matching it keeps the
// only behavioural differences the two intended ones.
//
// The exception is Proxy, which DefaultTransport sets to
// http.ProxyFromEnvironment and this leaves nil. With a proxy configured,
// every connection dials the proxy, so Control would be inspecting the
// proxy's address while the proxy resolved and reached the real target — the
// filter would pass on every request and protect nothing. Ignoring
// HTTP_PROXY/HTTPS_PROXY for MRF downloads is the honest version of that: the
// control either works or is visibly absent, rather than silently present and
// useless. A deployment that genuinely must fetch through a proxy has to move
// this check into the proxy's own egress rules.
func newFetchClient(control func(network, address string, c syscall.RawConn) error) *http.Client {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   control,
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
		CheckRedirect: checkRedirect,
	}
}

// publicOnlyClient is what every real fetch uses: it refuses, at dial time,
// to connect to anything that is not on the public internet.
var publicOnlyClient = newFetchClient(refuseNonPublicAddress)

// unrestrictedClient drops that filter. It is reachable only when the caller
// passes allowPrivateAddresses, which comes from ALLOW_PRIVATE_MRF_ADDRESSES
// and defaults to false — see the flag's own paragraph on Fetch for why it
// exists at all and why turning it on in a deployment that lets untrusted
// users add hospitals reopens the hole.
var unrestrictedClient = newFetchClient(nil)

// Fetch downloads a hospital's MRF and hands back a streaming Source over it,
// plus the io.Closer the caller must close when it is done reading. It is
// called from internal/validation/worker.go, which is the only caller.
//
// Nothing here buffers the whole file: the response body is wrapped in a
// sizeCappedReader and passed straight to Open, so a four-gigabyte MRF costs
// this process a 64 KiB read buffer rather than four gigabytes of memory. The
// returned Closer is the raw response body rather than the capped reader
// because closing the underlying connection is what actually stops a download
// that is still arriving.
//
// allowPrivateAddresses disables the non-public address filter described on
// refuseNonPublicAddress. It exists because loopback is exactly where a test
// server lives: internal/validation's end-to-end test serves its fixture from
// httptest.NewServer on 127.0.0.1, and a local dev setup pointing at a file
// server on localhost is the same shape. Production passes false — the
// default everywhere, including when ALLOW_PRIVATE_MRF_ADDRESSES is unset —
// and should keep passing false, because with it on any signed-up user can
// use this server as a proxy into whatever network it runs in.
func Fetch(ctx context.Context, url string, maxBytes int64, allowPrivateAddresses bool) (Source, io.Closer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, &FetchError{
			Public: "That MRF URL could not be parsed as a URL.",
			Err:    fmt.Errorf("mrf: building request for %s: %w", url, err),
		}
	}
	req.Header.Set("User-Agent", "MRFSentinel/1.0 (hospital price transparency compliance checker; +https://github.com/bobbylon127/mrfsentinel)")
	req.Header.Set("Accept", "application/json, text/csv, */*")

	client := publicOnlyClient
	if allowPrivateAddresses {
		client = unrestrictedClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, &FetchError{
			Public: unreachableMessage,
			Err:    fmt.Errorf("mrf: fetching %s: %w", url, err),
		}
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		// The status code is safe to repeat where the transport error was
		// not: the filter above means a response only ever comes back from a
		// publicly reachable host, which the user could have fetched
		// themselves, and "your URL returns 403" is the single most useful
		// thing this app can tell someone whose MRF sits behind a login.
		return nil, nil, &FetchError{
			Public: fmt.Sprintf("The MRF URL returned HTTP %d. A published MRF must be downloadable without authentication and return 200.", resp.StatusCode),
			Err:    fmt.Errorf("mrf: %s returned HTTP %d, expected 200", url, resp.StatusCode),
		}
	}

	capped := &sizeCappedReader{r: resp.Body, max: maxBytes}

	src, err := Open(capped)
	if err != nil {
		_ = resp.Body.Close()
		// Open's errors describe the bytes the hospital published — "file is
		// empty", an unrecognised format — so unlike a dial error they say
		// nothing about this server's network and go through verbatim.
		return nil, nil, &FetchError{Public: err.Error(), Err: err}
	}
	return src, resp.Body, nil
}

// sizeCappedReader stops a download at max bytes. This is the enforcement
// point for MAX_MRF_MEBIBYTES (see internal/config), wrapping the response
// body so the cap applies to whatever the parser streams rather than needing
// every parser to count for itself.
type sizeCappedReader struct {
	r    io.Reader
	max  int64
	read int64
}

// Read implements io.Reader, truncating the caller's buffer so the very last
// permitted read cannot overshoot, and failing once the limit is reached.
// Returning an error rather than io.EOF is deliberate: EOF would look to the
// parser like a file that legitimately ended, and a truncated MRF would then
// be scored as if it were complete.
func (s *sizeCappedReader) Read(p []byte) (int, error) {
	if s.read >= s.max {
		return 0, fmt.Errorf("mrf: file exceeds the configured %d byte limit (MAX_MRF_MEBIBYTES)", s.max)
	}
	if remaining := s.max - s.read; int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := s.r.Read(p)
	s.read += int64(n)
	return n, err
}
