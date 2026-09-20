package mrf

import (
	"bufio"
	"fmt"
	"io"
)

// Open detects whether r holds a JSON or CSV-formatted MRF and returns a
// Source streaming it. Detection reads at most a few bytes of lookahead
// (via bufio.Reader.Peek, which never discards what it peeked at) — the
// file itself is still read incrementally by whichever parser Open hands
// off to, not buffered here.
func Open(r io.Reader) (Source, error) {
	br := bufio.NewReaderSize(r, 64*1024)

	for {
		b, err := br.Peek(1)
		if err != nil {
			if err == io.EOF {
				return nil, &ParseError{Format: FormatUnknown, Err: fmt.Errorf("file is empty")}
			}
			return nil, &ParseError{Format: FormatUnknown, Err: fmt.Errorf("reading first byte: %w", err)}
		}
		switch b[0] {
		case ' ', '\t', '\r', '\n':
			_, _ = br.Discard(1)
			continue
		}
		break
	}

	first, err := br.Peek(1)
	if err != nil {
		return nil, &ParseError{Format: FormatUnknown, Err: fmt.Errorf("reading first byte: %w", err)}
	}
	if first[0] == '{' {
		return openJSON(br)
	}
	return openCSV(br)
}
