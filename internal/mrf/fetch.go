package mrf

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// Fetch downloads the MRF published at url and returns a Source streaming
// it, plus the underlying io.Closer the caller must Close once it's done
// reading rows (Source itself has no Close — see the package doc for why
// that's a deliberate design, not an oversight: rows are read lazily, well
// after Fetch has already returned, so the thing keeping the HTTP
// connection open has to outlive this function call).
//
// maxBytes enforces a hard ceiling on how much of the response body gets
// read. Spring Boot gives you a request/response size limit as a property
// you set once (server.tomcat.max-swallow-size and friends); net/http has
// no equivalent default, so this wraps the response body in a reader that
// enforces one explicitly.
func Fetch(ctx context.Context, url string, maxBytes int64) (Source, io.Closer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("mrf: building request for %s: %w", url, err)
	}
	req.Header.Set("User-Agent", "MRFSentinel/1.0 (hospital price transparency compliance checker; +https://github.com/bobbylon127/mrfsentinel)")
	req.Header.Set("Accept", "application/json, text/csv, */*")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("mrf: fetching %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, nil, fmt.Errorf("mrf: %s returned HTTP %d, expected 200", url, resp.StatusCode)
	}

	capped := &sizeCappedReader{r: resp.Body, max: maxBytes}

	src, err := Open(capped)
	if err != nil {
		_ = resp.Body.Close()
		return nil, nil, err
	}
	return src, resp.Body, nil
}

// sizeCappedReader fails with an explicit, named error the instant more
// than max bytes have been read from it, rather than letting the caller
// find out the hard way (an OOM kill, or an unbounded disk write) partway
// through a multi-gigabyte file.
type sizeCappedReader struct {
	r    io.Reader
	max  int64
	read int64
}

// Read passes through to the wrapped reader until max bytes have been
// consumed in total, then fails every subsequent call. It also trims the
// caller's buffer so the cap is never overshot within a single Read, which
// matters because the underlying reader here is a network socket delivering
// whatever happens to have arrived.
//
// Note this reports an error rather than a clean io.EOF: a truncated file
// silently treated as a complete one would produce a confidently wrong
// compliance report, which is worse than no report at all.
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
