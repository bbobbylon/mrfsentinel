package mrf

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

// These tests need no database and no network. Everything reachable from a
// test process is loopback, which is precisely the case the filter under
// test refuses — so the end-to-end tests below assert on refusal, and the
// ranges no test process can dial (RFC 1918, the cloud metadata endpoint,
// CGNAT) are covered by exercising the predicate directly.

// TestIsPublicAddr covers the address classifier that decides whether a
// resolved MRF address may be dialed at all.
//
// The interesting rows are the ones that look public at a glance. 0x7f000001
// written as ::ffff:127.0.0.1 is loopback that no IPv6 predicate catches
// until Unmap runs; 169.254.169.254 is the address that makes SSRF worth
// exploiting on AWS, GCP and Azure, and it is caught as link-local rather
// than by name; 100.100.100.200 is Alibaba Cloud's equivalent, which is why
// CGNAT space is on the blocklist at all.
func TestIsPublicAddr(t *testing.T) {
	tests := []struct {
		addr string
		want bool
		why  string
	}{
		{"8.8.8.8", true, "an ordinary public address"},
		{"93.184.216.34", true, "an ordinary public address"},
		{"2606:4700:4700::1111", true, "an ordinary public IPv6 address"},

		{"127.0.0.1", false, "loopback"},
		{"127.0.0.53", false, "loopback, the systemd-resolved stub"},
		{"::1", false, "IPv6 loopback"},
		{"::ffff:127.0.0.1", false, "loopback written as an IPv4-mapped IPv6 address"},
		{"0.0.0.0", false, "unspecified"},
		{"::", false, "IPv6 unspecified"},
		{"10.0.0.5", false, "RFC 1918 private"},
		{"172.16.31.9", false, "RFC 1918 private"},
		{"192.168.1.1", false, "RFC 1918 private"},
		{"fd00::1", false, "IPv6 unique-local"},
		{"169.254.169.254", false, "the cloud instance-metadata endpoint, caught as link-local"},
		{"fe80::1", false, "IPv6 link-local"},
		{"100.100.100.200", false, "Alibaba Cloud metadata, inside CGNAT space"},
		{"100.64.0.1", false, "carrier-grade NAT"},
		{"224.0.0.1", false, "multicast"},
		{"ff02::1", false, "IPv6 link-local multicast"},
		{"192.0.2.1", false, "TEST-NET-1"},
		{"198.18.0.1", false, "benchmarking range"},
		{"240.0.0.1", false, "reserved"},
		{"2001:db8::1", false, "documentation range"},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			addr, err := netip.ParseAddr(tt.addr)
			if err != nil {
				t.Fatalf("test fixture %q is not a valid address: %v", tt.addr, err)
			}
			if got := isPublicAddr(addr); got != tt.want {
				t.Errorf("isPublicAddr(%s) = %v, want %v — %s", tt.addr, got, tt.want, tt.why)
			}
		})
	}
}

// TestIsPublicAddr_RejectsTheZeroValue pins the one input that reaches this
// function without coming from a parser. A zero netip.Addr is not a real
// address, and defaulting it to "public" would mean any future caller that
// forgot to check a parse error got a free pass through the filter.
func TestIsPublicAddr_RejectsTheZeroValue(t *testing.T) {
	if isPublicAddr(netip.Addr{}) {
		t.Error("the zero netip.Addr was treated as a public address")
	}
}

// TestRefuseNonPublicAddress covers the Control hook itself, with the
// arguments net.Dialer passes it: a network name and an already-resolved
// host:port. This is the layer the end-to-end tests cannot reach for private
// ranges, since a test process has no way to make a hospital URL resolve to
// 10.0.0.5.
func TestRefuseNonPublicAddress(t *testing.T) {
	tests := []struct {
		name      string
		network   string
		address   string
		wantBlock bool
	}{
		{"public address", "tcp4", "8.8.8.8:443", false},
		{"public IPv6 address", "tcp6", "[2606:4700:4700::1111]:443", false},
		{"loopback", "tcp4", "127.0.0.1:8080", true},
		{"IPv6 loopback", "tcp6", "[::1]:8080", true},
		{"private range", "tcp4", "10.0.0.5:22", true},
		{"cloud metadata endpoint", "tcp4", "169.254.169.254:80", true},
		{"unix socket", "unix", "/var/run/docker.sock", true},
		{"unparseable address", "tcp", "not-an-address", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := refuseNonPublicAddress(tt.network, tt.address, nil)
			if tt.wantBlock {
				if err == nil {
					t.Fatalf("dial to %s/%s was allowed", tt.network, tt.address)
				}
				if !errors.Is(err, ErrBlockedAddress) {
					t.Errorf("error does not wrap ErrBlockedAddress: %v", err)
				}
				return
			}
			if err != nil {
				t.Errorf("dial to %s/%s was refused: %v", tt.network, tt.address, err)
			}
		})
	}
}

// TestFetch_RefusesALoopbackURL is the end-to-end version: a real server, a
// real client, and the production setting. The handler flag is the assertion
// that matters — a run marked failed only proves the user saw an error,
// while an unreached handler proves the request was never sent, which is the
// difference between reporting an SSRF and preventing one.
func TestFetch_RefusesALoopbackURL(t *testing.T) {
	// atomic because the handler runs on the server's goroutine: if the
	// filter ever regressed, a plain bool would surface as a data race under
	// -race instead of the clear failure this test is meant to give.
	var handlerRan atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerRan.Store(true)
		_, _ = io.WriteString(w, `{"hospital_name":"Test"}`)
	}))
	defer srv.Close()

	_, _, err := Fetch(context.Background(), srv.URL+"/mrf.json", 8<<20, false)
	if err == nil {
		t.Fatal("Fetch succeeded against a loopback URL with the address filter on")
	}
	if handlerRan.Load() {
		t.Error("the server was actually reached — the filter did not stop the connection")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("error does not wrap ErrBlockedAddress: %v", err)
	}
}

// TestFetch_BlockedURLGivesTheGenericPublicMessage checks the half of the
// fix that the block itself does not deliver. internal/web renders a failed
// run's error_message on the report page, so a refusal that said "blocked:
// 10.0.0.5" would hand back exactly the fact the filter existed to withhold.
func TestFetch_BlockedURLGivesTheGenericPublicMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	_, _, err := Fetch(context.Background(), srv.URL+"/mrf.json", 8<<20, false)
	if err == nil {
		t.Fatal("Fetch succeeded against a loopback URL with the address filter on")
	}

	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("Fetch returned %T, not a *FetchError, so the worker has no safe message to store: %v", err, err)
	}
	if fetchErr.Public != unreachableMessage {
		t.Errorf("public message = %q, want the generic %q", fetchErr.Public, unreachableMessage)
	}
	for _, leak := range []string{"127.0.0.1", "::1", "blocked", "dial"} {
		if strings.Contains(strings.ToLower(fetchErr.Public), leak) {
			t.Errorf("public message contains %q: %q", leak, fetchErr.Public)
		}
	}
	// The detail must still be there for the log — a fix that made the
	// operator's error as vague as the user's would be its own problem.
	if !strings.Contains(fetchErr.Error(), "127.0.0.1") && !strings.Contains(fetchErr.Error(), "::1") {
		t.Errorf("logged error names no address, leaving an operator nothing to debug: %v", fetchErr)
	}
}

// TestFetch_AllowsLoopbackWhenPermitted is the escape hatch working. Without
// it internal/validation's end-to-end test could not run at all, since its
// fixture is served from httptest — so this is also the test that fails
// first if the flag is ever quietly dropped.
func TestFetch_AllowsLoopbackWhenPermitted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"hospital_name":"Test General Hospital"}`)
	}))
	defer srv.Close()

	src, closer, err := Fetch(context.Background(), srv.URL+"/mrf.json", 8<<20, true)
	if err != nil {
		t.Fatalf("Fetch failed with allowPrivateAddresses=true: %v", err)
	}
	defer func() { _ = closer.Close() }()

	if got := src.Format(); got != FormatJSON {
		t.Errorf("detected format = %v, want %v", got, FormatJSON)
	}
}

// TestFetch_NonOKStatusKeepsTheStatusInThePublicMessage covers the one
// detail deliberately left visible. Once the filter guarantees a response
// can only come from a publicly reachable host, the status code is something
// the user could have looked up themselves — and "your URL returns 403" is
// the most useful thing this app can say to someone whose MRF sits behind a
// login, so it is worth keeping.
func TestFetch_NonOKStatusKeepsTheStatusInThePublicMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, _, err := Fetch(context.Background(), srv.URL+"/mrf.json", 8<<20, true)
	if err == nil {
		t.Fatal("Fetch succeeded against a 403 response")
	}

	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("Fetch returned %T, not a *FetchError: %v", err, err)
	}
	if !strings.Contains(fetchErr.Public, "403") {
		t.Errorf("public message does not mention the status code: %q", fetchErr.Public)
	}
}

// TestFetch_StopsAfterTooManyRedirects pins the hop cap. The server here
// redirects to itself forever, which without a bound is a request that never
// finishes — the fetch timeout in internal/validation would eventually kill
// it, but only after holding a goroutine and a connection for fifteen
// minutes.
//
// It runs with the filter off because a redirect loop needs a server that
// can actually be reached, and every address a test process can bind is one
// the filter refuses.
func TestFetch_StopsAfterTooManyRedirects(t *testing.T) {
	var hops atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops.Add(1)
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	defer srv.Close()

	_, _, err := Fetch(context.Background(), srv.URL+"/mrf.json", 8<<20, true)
	if err == nil {
		t.Fatal("Fetch followed an endless redirect loop to completion")
	}
	if got := hops.Load(); got > maxFetchRedirects+1 {
		t.Errorf("server saw %d requests, want at most %d — the redirect cap is not being applied", got, maxFetchRedirects+1)
	}
}

// TestCheckRedirect covers the scheme half of the redirect policy directly.
// net/http refuses a non-http(s) redirect on its own, so this is defence in
// depth; testing it here is what keeps that second layer from rotting
// unnoticed behind the first.
func TestCheckRedirect(t *testing.T) {
	req := func(rawURL string) *http.Request {
		r, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			t.Fatalf("building request for %q: %v", rawURL, err)
		}
		return r
	}

	if err := checkRedirect(req("https://example.com/mrf.json"), nil); err != nil {
		t.Errorf("an https redirect was refused: %v", err)
	}
	if err := checkRedirect(req("http://example.com/mrf.json"), nil); err != nil {
		t.Errorf("an http redirect was refused: %v", err)
	}
	if err := checkRedirect(req("file:///etc/passwd"), nil); err == nil {
		t.Error("a file:// redirect was allowed")
	}

	via := make([]*http.Request, maxFetchRedirects)
	if err := checkRedirect(req("https://example.com/mrf.json"), via); err == nil {
		t.Errorf("a redirect at hop %d was allowed, past the cap of %d", len(via), maxFetchRedirects)
	}
}

// TestSizeCappedReader_StopsAtTheLimit covers MAX_MRF_MEBIBYTES's
// enforcement point. The failure mode it rules out is subtle: returning
// io.EOF at the cap would look to the parser like a file that simply ended,
// and a truncated MRF would then be scored — and possibly marked compliant —
// on the fraction that fitted.
func TestSizeCappedReader_StopsAtTheLimit(t *testing.T) {
	r := &sizeCappedReader{r: strings.NewReader(strings.Repeat("x", 100)), max: 10}

	got, err := io.ReadAll(r)
	if err == nil {
		t.Fatal("reading past the cap returned no error, so a truncated file would be parsed as complete")
	}
	if errors.Is(err, io.EOF) {
		t.Error("the cap was reported as io.EOF, which a parser reads as a clean end of file")
	}
	if len(got) > 10 {
		t.Errorf("read %d bytes, want at most the 10-byte cap", len(got))
	}
}

// TestSizeCappedReader_PassesShortFilesThrough is the other half: a file
// comfortably under the limit must arrive byte for byte.
func TestSizeCappedReader_PassesShortFilesThrough(t *testing.T) {
	const body = `{"hospital_name":"Test"}`
	r := &sizeCappedReader{r: strings.NewReader(body), max: 8 << 20}

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading a file well under the cap failed: %v", err)
	}
	if string(got) != body {
		t.Errorf("read %q, want %q", got, body)
	}
}
