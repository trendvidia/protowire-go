// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

// Streaming consumption for the `@dataset` directive (draft §3.4.4).
//
// The materializing path (pxf.UnmarshalFull / pxf.Parse) reads an
// entire document into memory and produces a full DatasetDirective with
// all rows resident. That works for small datasets and breaks for the
// CSV-replacement workload @dataset was designed to serve. DatasetReader
// provides the streaming alternative: it pulls bytes from an io.Reader
// on demand and yields one DatasetRow per Next() call, with working-set
// memory bounded by the size of the largest single row.
//
// Per the spec: a streaming API MUST enforce per-row arity on each row
// as it is consumed (not deferred to end-of-input), and MUST yield rows
// in source order. Both invariants fall out of the implementation here:
// the row-boundary scanner produces one (...) slice at a time, and the
// same parseDatasetRow used by Parse() decodes it.

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// defaultHeaderMaxBytes caps the byte budget for the @dataset header
// (leading directives + `@dataset TYPE (col1, col2, ...)`). Real headers
// are tiny — a few hundred bytes at most. The cap exists to fail-fast
// on misuse: a DatasetReader pointed at a multi-gigabyte document with
// no `@dataset` directive shouldn't OOM trying to find one.
const defaultHeaderMaxBytes = 64 * 1024

// streamPullSize is the chunk size for io.Reader pulls. Larger reduces
// syscall pressure; smaller bounds per-row peak buffer occupancy. 4 KiB
// matches bufio.Reader's default and is generous for typical row sizes.
const streamPullSize = 4096

// ErrNoDataset is returned by NewDatasetReader when the input contains no
// @dataset directive before EOF. Inspect with errors.Is.
var ErrNoDataset = errors.New("pxf: no @dataset directive in stream")

// DatasetReader streams rows of a single `@dataset` directive from an
// io.Reader (draft §3.4.4 "Streaming consumption"). Use it for
// datasets too large to materialize via [Parse] / [UnmarshalFull].
//
// A DatasetReader is positioned at the first row after [NewDatasetReader]
// returns. Call [DatasetReader.Next] in a loop until it returns
// [io.EOF]; the table's row sequence is exhausted at that point.
//
// For documents containing multiple `@dataset` directives, call
// NewDatasetReader again on the same underlying io.Reader to read the
// next table — the previous reader leaves the underlying reader
// positioned just past its last row.
//
// A DatasetReader is NOT safe for concurrent use.
type DatasetReader struct {
	src      io.Reader
	pending  []byte // bytes pulled from src but not yet consumed
	srcEOF   bool   // src.Read has returned io.EOF
	typ      string
	columns  []string
	dirs     []Directive
	finished bool     // Next() has returned io.EOF
	err      error    // sticky error
	lim      limits   // the options' limits; lim.maxMessageSize caps pending while a row boundary is sought
	base     Position // document position of pending[0], so rows and errors carry Parse's positions

	// The row scan resumes where the previous pull left it, so a row is
	// lexed once however many reads it arrives in (#148). scanPos is the
	// start of the first token not yet known to be complete; rowStart is
	// the offset of the current row's '(' , or -1 between rows. Both
	// are offsets into pending.
	scanPos  int
	rowStart int

	// While a token runs past the buffer's end, the scan does not lex it
	// again until bytes arrive that could end it (mayEndToken): waitAt is
	// the token's start and waitFrom the buffer length already checked,
	// or 0 when no token is waiting. Without this, one long literal costs
	// a re-lex of the whole literal on every read.
	waitAt   int
	waitFrom int

	// scanned counts the bytes the row scan has lexed, for the tests
	// that hold it linear in the input.
	scanned int

	lx lexer // the scan's lexer, reset for each token (nextToken)

	// pathsChecked is the last row type Scan checked columns against
	// (checkColumnPaths), so a table of one type checks them once.
	pathsChecked protoreflect.MessageDescriptor
}

// NewDatasetReader consumes any leading directives (`@type`, `@<name>`,
// etc.) and the `@dataset TYPE ( cols )` header, returning a reader
// positioned at the first row.
//
// Returns [ErrNoDataset] if the input is a well-formed document with no
// `@dataset` directive. A malformed one, a header cut short included,
// returns the error [Parse] returns for it; a read failure is returned
// as is.
//
// The header (everything from the start of the input up through and
// including the `)` of the column list) must fit in 64 KiB. This is a
// fail-fast bound — real headers are tiny.
func NewDatasetReader(r io.Reader) (*DatasetReader, error) {
	return UnmarshalOptions{}.NewDatasetReader(r)
}

// NewDatasetReader is [NewDatasetReader] under the options' limits: the
// bytes held while a row boundary is sought are capped at the options'
// MaxMessageSize, so a stream that never ends a row cannot grow the
// buffer without bound, and the header and every row are parsed under
// its MaxNestingDepth and MaxBytesLiteralLength, as [ParseOptions.Parse]
// parses them. [DatasetReader.Scan] binds a row under every limit but
// MaxMessageSize, as Unmarshal would bind the body the row stands for.
func (o UnmarshalOptions) NewDatasetReader(r io.Reader) (*DatasetReader, error) {
	tr := &DatasetReader{src: r, lim: o.limits(), base: Position{Line: 1, Column: 1}, rowStart: -1}
	if t, ok := r.(*datasetTail); ok {
		tr.base = t.at
	}
	if err := tr.readHeader(); err != nil {
		return nil, err
	}
	return tr, nil
}

// Type returns the row message type declared by the @dataset header
// (e.g. "trades.v1.Trade").
func (tr *DatasetReader) Type() string { return tr.typ }

// Columns returns the column field names declared by the @dataset
// header, in source order.
func (tr *DatasetReader) Columns() []string { return tr.columns }

// Directives returns the side-channel directives (`@<name>` /
// `@entry` / etc., NOT `@type` or `@dataset`) that appeared before the
// `@dataset` header. The slice is stable for the lifetime of the
// reader. Useful for consumers that attach per-table metadata via a
// preceding directive.
func (tr *DatasetReader) Directives() []Directive { return tr.dirs }

// Tail returns an [io.Reader] that yields the bytes the DatasetReader
// has buffered but not consumed, followed by any remaining bytes from
// the underlying source. Use it to chain a second [NewDatasetReader]
// call for documents containing multiple `@dataset` directives:
//
//	tr1, err := pxf.NewDatasetReader(src)
//	// ... iterate tr1.Next() to io.EOF ...
//	tr2, err := pxf.NewDatasetReader(tr1.Tail())
//
// Tail MUST only be called after Next has returned [io.EOF]. Calling
// it earlier returns bytes the current reader still intends to
// consume, which will desync the next reader. The returned io.Reader
// is unbuffered; consumers needing bufio semantics should wrap it
// themselves. A reader built directly on it reports document positions,
// as [Parse] does; one built on a wrapper counts from the Tail's start.
func (tr *DatasetReader) Tail() io.Reader {
	if len(tr.pending) == 0 {
		return &datasetTail{Reader: tr.src, at: tr.base}
	}
	return &datasetTail{Reader: io.MultiReader(bytes.NewReader(tr.pending), tr.src), at: tr.base}
}

// datasetTail is the reader Tail returns. It carries the document
// position its first byte sits at, so the next table's rows and errors
// carry Parse's positions.
type datasetTail struct {
	io.Reader
	at Position
}

// Next reads the next row. Returns [io.EOF] when the table's row
// sequence is exhausted: at the end of the input, or before the next
// directive (read it via [DatasetReader.Tail]). Anything else that is
// not a well-formed row, a row cut short at the end of the input
// included, is an error, and it is the error [Parse] reports for the
// same input, at the same document position. After EOF (or any other
// error), all subsequent calls return the same error.
//
// The returned DatasetRow's Cells slice is freshly allocated and owned
// by the caller; reading the next row does not invalidate it.
func (tr *DatasetReader) Next() (DatasetRow, error) {
	if tr.err != nil {
		return DatasetRow{}, tr.err
	}
	if tr.finished {
		return DatasetRow{}, io.EOF
	}
	if row, end, ok := tr.tryWholeRow(); ok {
		tr.consume(end)
		return row, nil
	}
	for {
		kind, start, end := tr.scanRow()
		switch kind {
		case scanRow:
			row, err := tr.parseRow(start, end)
			tr.consume(end)
			if err != nil {
				return tr.fail(err)
			}
			return *row, nil
		case scanBad:
			// Parse the rest of the buffer, not just through the token
			// that stopped the scan: the lexer may reject a token before
			// its end (an oversized bytes literal), and cut there, it
			// would read as unterminated instead.
			return tr.fail(tr.rowError(start, len(tr.pending)))
		case scanEnd:
			tr.consume(start)
			tr.finished = true
			return DatasetRow{}, io.EOF
		case scanNotRow:
			return tr.fail(errorf(tr.posAt(start), errDatasetBesideEntries))
		}
		// scanNeedMore. At the end of the source, only whitespace and
		// comments may remain; anything else is a row cut short.
		if tr.srcEOF {
			if start >= 0 {
				return tr.fail(tr.rowError(start, len(tr.pending)))
			}
			tr.consume(len(tr.pending))
			tr.finished = true
			return DatasetRow{}, io.EOF
		}
		if perr := tr.pull(streamPullSize); perr != nil {
			return tr.fail(perr)
		}
	}
}

// tryWholeRow is the fast path for a row already in the buffer: the
// row parser runs on the buffer directly, so the row is lexed once. If
// it parses a complete row, every token in it ended before its ')', so
// the row is the one Parse reads; it returns that row and the offset
// past it. Anything else (a row cut by a read, a malformed row, no row)
// returns ok=false, and scanRow finds the boundary with the lexer. It
// is tried once per row, before the scan starts on it, so a row that
// spans many reads is still lexed in linear time.
func (tr *DatasetReader) tryWholeRow() (row DatasetRow, end int, ok bool) {
	if tr.rowStart >= 0 || tr.waitFrom > 0 {
		return DatasetRow{}, 0, false
	}
	at := tr.posAt(tr.scanPos)
	p := newParserAt(tr.pending[tr.scanPos:], at, tr.lim)
	if p.current.Kind != LPAREN {
		return DatasetRow{}, 0, false
	}
	r, endOffset, err := p.parseDatasetRow(len(tr.columns))
	if err != nil {
		return DatasetRow{}, 0, false
	}
	return *r, tr.scanPos + endOffset - at.Offset, true
}

// fail records err as the reader's sticky error and returns it.
func (tr *DatasetReader) fail(err error) (DatasetRow, error) {
	tr.err = err
	return DatasetRow{}, err
}

// posAt returns the document position of pending[i].
func (tr *DatasetReader) posAt(i int) Position {
	p := tr.base
	for _, ch := range tr.pending[:i] {
		if ch == '\n' {
			p.Line++
			p.Column = 1
		} else {
			p.Column++
		}
	}
	p.Offset += i
	return p
}

// consume drops the first n pending bytes, advancing base past them.
func (tr *DatasetReader) consume(n int) {
	tr.base = tr.posAt(n)
	tr.pending = tr.pending[n:]
	tr.scanPos = max(tr.scanPos-n, 0)
	if tr.rowStart >= 0 {
		tr.rowStart -= n
		if tr.rowStart < 0 {
			tr.rowStart = -1
		}
	}
}

// parseRow parses pending[start:end] as one row with the parser Parse
// uses, at its document position.
func (tr *DatasetReader) parseRow(start, end int) (*DatasetRow, error) {
	p := newParserAt(tr.pending[start:end], tr.posAt(start), tr.lim)
	if p.current.Kind != LPAREN {
		// Parse reports any token here, a lexical error included, as a
		// top-level entry beside the dataset.
		return nil, errorf(p.current.Pos, errDatasetBesideEntries)
	}
	row, _, err := p.parseDatasetRow(len(tr.columns))
	return row, err
}

// rowError reports why the row starting at pending[start] is malformed.
// The scan has seen every token from the row's '(' up to the one that
// stopped it (a stray '(', a token the lexer rejects, or the end of the
// input), and no ')' that closes the row. parseDatasetRow fails on
// pending[start:end] at the token where Parse fails on the whole
// document, because the tokens before it are the same.
func (tr *DatasetReader) rowError(start, end int) error {
	_, err := tr.parseRow(start, end)
	if err == nil {
		// Unreachable: the slice has no closing ')'.
		err = io.ErrUnexpectedEOF
	}
	return err
}

// pull reads up to n bytes from src into pending. Sets srcEOF when
// src is exhausted. Returns nil on success (including EOF reached);
// returns a non-nil error only on a non-EOF read failure.
func (tr *DatasetReader) pull(n int) error {
	if tr.srcEOF {
		return nil
	}
	// Read into the buffer's spare capacity: a fresh buffer per read
	// costs an allocation per call, which a reader returning a byte at a
	// time turns into one per byte. Growing only ever writes past the
	// current end, so rows already returned (which may view these bytes)
	// are untouched.
	if cap(tr.pending)-len(tr.pending) < n {
		grown := make([]byte, len(tr.pending), 2*len(tr.pending)+n)
		copy(grown, tr.pending)
		tr.pending = grown
	}
	read, err := tr.src.Read(tr.pending[len(tr.pending) : len(tr.pending)+n])
	if read > 0 {
		tr.pending = tr.pending[:len(tr.pending)+read]
		if len(tr.pending) > tr.lim.maxMessageSize {
			return fmt.Errorf("dataset input holds %d bytes without a row boundary, MaxMessageSize=%d", len(tr.pending), tr.lim.maxMessageSize)
		}
	}
	if errors.Is(err, io.EOF) {
		tr.srcEOF = true
		return nil
	}
	return err
}

// readHeader pulls bytes until it finds the closing `)` of the
// @dataset column list, then uses the existing parser to extract typ,
// columns, and any preceding side-channel directives.
func (tr *DatasetReader) readHeader() error {
	for {
		headerEnd, found, bad := tr.scanHeader()
		if bad > 0 {
			// The lexer rejected a token before the header ended. Every
			// token before it is complete, so Parse fails at or before
			// it (the whole buffer, for the reason scanBad gives).
			if _, perr := tr.parseHeader(tr.pending); perr != nil {
				return perr
			}
		}
		if found {
			// Parse the header prefix as a (rowless) PXF document.
			// parseDocument is happy with an @dataset directive that has
			// no rows yet, and validates everything we care about
			// (leading-directive shape, @type / @dataset conflict,
			// repeated or overlapping column paths, etc.).
			doc, perr := tr.parseHeader(tr.pending[:headerEnd])
			if perr != nil {
				return perr
			}
			if len(doc.Datasets) == 0 {
				// Should not happen — scanHeader found an @dataset,
				// but defensive.
				return ErrNoDataset
			}
			ds := doc.Datasets[0]
			tr.typ = ds.Type
			tr.columns = ds.Columns
			tr.dirs = doc.Directives
			tr.consume(headerEnd)
			return nil
		}
		if tr.srcEOF {
			// The input ended without a complete header. A malformed
			// document (a header cut short, a literal that never
			// closes) reports what Parse reports for it; only a
			// well-formed one is a document without a dataset.
			if _, perr := tr.parseHeader(tr.pending); perr != nil {
				return perr
			}
			return ErrNoDataset
		}
		if len(tr.pending) >= defaultHeaderMaxBytes {
			return fmt.Errorf("pxf: @dataset header exceeds %d bytes; raise the budget or check that the input begins with `@dataset TYPE (cols)`", defaultHeaderMaxBytes)
		}
		if err := tr.pull(streamPullSize); err != nil {
			return err
		}
	}
}

// parseHeader parses input, which starts at the reader's base, as a
// document, the way Parse does.
func (tr *DatasetReader) parseHeader(input []byte) (*Document, error) {
	return newParserAt(input, tr.base, tr.lim).parseDocument()
}

// rowScan classifies what scanRow found.
type rowScan int

const (
	// scanNeedMore: the input ends before the scan could decide. start
	// is the current row's '(' , or -1 between rows.
	scanNeedMore rowScan = iota
	// scanRow: pending[start:end] is a complete `( ... )` row.
	scanRow
	// scanBad: the row at start cannot close well; pending[start:end]
	// ends at the token that shows it (a stray '(' or a token the
	// lexer rejects). Parse it for the error.
	scanBad
	// scanEnd: a directive starts at start; the row sequence is over.
	scanEnd
	// scanNotRow: start holds something that is neither a row nor a
	// directive, which Parse rejects as a top-level entry.
	scanNotRow
)

// nextToken lexes the token at pending[from:] and returns it with the
// offset just past it, or ok=false when the token runs to the end of
// the buffer and more input could still extend it (an identifier, a
// number, a string or a comment without its end). Offsets are into
// pending. This is the lexer Parse uses, so the scan and Parse can
// never disagree about where a token ends (#149).
func (tr *DatasetReader) nextToken(from int) (tok Token, end int, ok bool) {
	// One lexer value, reset per token, so the scan allocates nothing
	// per token beyond what the token itself holds.
	tr.lx = lexer{input: tr.pending[from:], line: 1, col: 1, base: from, maxBytesLiteral: tr.lim.maxBytesLiteral, noValue: true}
	lx := &tr.lx
	tok = lx.Next()
	end = from + lx.pos
	tr.scanned += end - from
	if tr.srcEOF {
		return tok, end, true
	}
	return tok, end, tokenComplete(tok, tr.pending, end)
}

// illegalNearEnd is how close to the buffer's end a token the lexer
// rejects must stop for the rejection to be possibly the lexer running
// out of input rather than a defect: an unterminated triple-quoted
// string stops two bytes short of the end, a block comment one, an
// escape sequence up to its own length (`\U` and eight hex digits).
const illegalNearEnd = 16

// tokenComplete reports whether more input could change tok, which the
// lexer produced from buf and which ends at end. Punctuation and a
// newline are whole as soon as they are read. A closed string is whole,
// except `""` at the very end, which may open a triple-quoted string.
// Any other token ending at the buffer's end may continue (an
// identifier, a number, a line comment, an unterminated literal), and a
// rejected token close to the end may be the lexer running out of
// input. Treating a token as incomplete only delays the scan's decision
// to the next read or the end of input; it never changes it.
func tokenComplete(tok Token, buf []byte, end int) bool {
	switch tok.Kind {
	case EOF:
		return false
	case LPAREN, RPAREN, LBRACE, RBRACE, LBRACKET, RBRACKET, COMMA, EQUALS, COLON, NEWLINE:
		return true
	case STRING, BYTES:
		return !(end == len(buf) && string(buf[tok.Pos.Offset:end]) == `""`)
	case ILLEGAL:
		return end < len(buf)-illegalNearEnd
	}
	return end < len(buf)
}

// scanRow finds the next row with the lexer, resuming at scanPos: a
// token known complete is never lexed again, so a row costs time linear
// in its size however many reads it arrives in (#148). Between rows it
// skips newlines and comments; '(' starts a row, a directive ends the
// sequence, and anything else is not a row. Inside a row it stops at
// the ')' that closes it, at a '(' (no value holds one), or at a token
// the lexer rejects.
func (tr *DatasetReader) scanRow() (kind rowScan, start, end int) {
	if tr.waitFrom > 0 && !tr.srcEOF && !tr.mayEndToken() {
		tr.waitFrom = len(tr.pending)
		return scanNeedMore, tr.rowStart, 0
	}
	tr.waitFrom = 0
	for {
		tok, tokEnd, ok := tr.nextToken(tr.scanPos)
		if !ok {
			tr.waitAt, tr.waitFrom = tok.Pos.Offset, len(tr.pending)
			return scanNeedMore, tr.rowStart, 0
		}
		if tok.Kind == EOF {
			// Only at the end of the source (nextToken).
			return scanNeedMore, tr.rowStart, 0
		}
		if tok.Kind == NEWLINE || tok.Kind == COMMENT {
			tr.scanPos = tokEnd
			continue
		}
		at := tok.Pos.Offset
		if tr.rowStart < 0 {
			switch tok.Kind {
			case LPAREN:
				tr.rowStart = at
				tr.scanPos = tokEnd
				continue
			case AT_TYPE, AT_DIRECTIVE, AT_DATASET, AT_PROTO:
				return scanEnd, at, 0
			default:
				return scanNotRow, at, 0
			}
		}
		switch tok.Kind {
		case RPAREN:
			start = tr.rowStart
			tr.rowStart, tr.scanPos = -1, tokEnd
			return scanRow, start, tokEnd
		case LPAREN, ILLEGAL:
			return scanBad, tr.rowStart, tokEnd
		}
		tr.scanPos = tokEnd
	}
}

// mayEndToken reports whether the bytes read since the waiting token was
// last lexed could end it: a closing quote for a string (one not
// escaped by an odd run of backslashes) or a newline, which a simple
// string cannot hold, `"""` for a triple-quoted one,
// a quote or newline for a bytes literal, a newline for a line comment,
// `*/` for a block comment, and for anything else a byte that cannot
// continue an identifier, number, timestamp or duration. It only
// decides when the lexer looks again, never what the token is; a byte
// that ends a row, ')', always counts, so a complete row is never left
// waiting for input.
func (tr *DatasetReader) mayEndToken() bool {
	buf, at, from := tr.pending, tr.waitAt, tr.waitFrom
	tok, fresh := buf[at:], buf[from:]
	switch {
	case bytes.HasPrefix(tok, []byte(`"""`)):
		return bytes.Contains(buf[max(from-2, at+3):], []byte(`"""`))
	case bytes.HasPrefix(tok, []byte(`"`)) && from-at > 2:
		for i, ch := range fresh {
			if ch == '\n' {
				return true // a simple string cannot hold one (#143)
			}
			if ch == '"' {
				n := 0
				for j := from + i - 1; j > at && buf[j] == '\\'; j-- {
					n++
				}
				if n%2 == 0 {
					return true
				}
			}
		}
		return false
	case bytes.HasPrefix(tok, []byte(`b"`)):
		return bytes.ContainsAny(fresh, "\"\n")
	case bytes.HasPrefix(tok, []byte("#")), bytes.HasPrefix(tok, []byte("//")):
		return bytes.IndexByte(fresh, '\n') >= 0
	case bytes.HasPrefix(tok, []byte("/*")):
		return bytes.Contains(buf[max(from-1, at+2):], []byte("*/"))
	}
	for _, ch := range fresh {
		if !(isIdentPart(ch) || ch == '.' || ch == ':' || ch == '+' || ch == '-' || ch >= 0x80) {
			return true
		}
	}
	return false
}

// scanHeader finds the end of the first `@dataset TYPE ( cols )` header
// with the lexer: the offset just past the ')' that closes the column
// list, the first ')' after an @dataset token. It returns found=false
// when the input ends first. bad is the offset just past a complete
// token the lexer rejected before the header ended, or 0; a header is
// at most defaultHeaderMaxBytes, so it is lexed from the start on every
// pull.
func (tr *DatasetReader) scanHeader() (end int, found bool, bad int) {
	inDataset := false
	for at := 0; ; {
		tok, tokEnd, ok := tr.nextToken(at)
		if !ok || tok.Kind == EOF {
			return 0, false, 0
		}
		switch {
		case tok.Kind == ILLEGAL:
			return 0, false, tokEnd
		case tok.Kind == AT_DATASET:
			inDataset = true
		case inDataset && tok.Kind == RPAREN:
			return tokEnd, true, 0
		}
		at = tokEnd
	}
}
