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
	tr := &DatasetReader{src: r, lim: o.limits(), base: Position{Line: 1, Column: 1}}
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
	for {
		kind, start, end := findNextRow(tr.pending)
		switch kind {
		case scanRow:
			row, err := tr.parseRow(start, end+1)
			tr.consume(end + 1)
			if err != nil {
				return tr.fail(err)
			}
			return *row, nil
		case scanBad:
			return tr.fail(tr.rowError(start, end+1))
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

// rowError reports why the row in pending[start:end] is malformed. The
// scan has already seen that it is (an unclosed row at the end of the
// source, a '(' inside a row, a broken literal), so parsing it fails at
// the same token Parse fails at.
func (tr *DatasetReader) rowError(start, end int) error {
	if _, err := tr.parseRow(start, end); err != nil {
		return err
	}
	return errorf(tr.posAt(start), "malformed @dataset row")
}

// pull reads up to n bytes from src into pending. Sets srcEOF when
// src is exhausted. Returns nil on success (including EOF reached);
// returns a non-nil error only on a non-EOF read failure.
func (tr *DatasetReader) pull(n int) error {
	if tr.srcEOF {
		return nil
	}
	buf := make([]byte, n)
	read, err := tr.src.Read(buf)
	if read > 0 {
		tr.pending = append(tr.pending, buf[:read]...)
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
		headerEnd, found, err := scanHeaderEnd(tr.pending)
		if err != nil {
			// The scan found a literal that can never close. Every token
			// before it is complete, so Parse fails at or before it.
			if _, perr := tr.parseHeader(tr.pending); perr != nil {
				return perr
			}
			return err
		}
		if found {
			// Parse the header prefix as a (rowless) PXF document.
			// parseDocument is happy with an @dataset directive that has
			// no rows yet, and validates everything we care about
			// (leading-directive shape, @type / @dataset conflict,
			// repeated or overlapping column paths, etc.).
			doc, perr := tr.parseHeader(tr.pending[:headerEnd+1])
			if perr != nil {
				return perr
			}
			if len(doc.Datasets) == 0 {
				// Should not happen — scanHeaderEnd found an @dataset,
				// but defensive.
				return ErrNoDataset
			}
			ds := doc.Datasets[0]
			tr.typ = ds.Type
			tr.columns = ds.Columns
			tr.dirs = doc.Directives
			tr.consume(headerEnd + 1)
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

// scanHeaderEnd searches input for the first complete `@dataset TYPE (
// cols )` directive and returns the index of the `)` that closes its
// column list. Returns (0, false, nil) if the input ends before the
// header is complete (caller should pull more bytes).
//
// This is a byte-level scan, not a full parse: it walks the input
// looking for the literal byte sequence "@dataset", then for the next
// `(`, then for the matching `)`, with string / bytes-literal /
// comment awareness so embedded parens or `@dataset` substrings inside
// strings or comments don't trip the scan.
func scanHeaderEnd(input []byte) (int, bool, error) {
	// Find the start of an `@dataset` token outside strings/comments.
	atIdx, found, err := findAtTable(input)
	if err != nil {
		return 0, false, err
	}
	if !found {
		return 0, false, nil
	}
	// Find the next `(` after the @dataset keyword (skipping the type
	// identifier and whitespace).
	lparen, found, err := findNextChar(input, atIdx+len("@dataset"), '(')
	if err != nil {
		return 0, false, err
	}
	if !found {
		return 0, false, nil
	}
	// Find the matching `)`.
	end, ok, err := findMatchingParenSafe(input, lparen)
	if err != nil {
		return 0, false, err
	}
	if !ok {
		return 0, false, nil
	}
	return end, true, nil
}

// findAtTable returns the byte offset of the next `@dataset` keyword
// outside of strings/comments. The match must be followed by a
// non-identifier byte (so we don't false-match `@tableau`).
func findAtTable(input []byte) (int, bool, error) {
	i := 0
	for i < len(input) {
		j, err := skipStringOrComment(input, i)
		if err != nil {
			return 0, false, err
		}
		if j == -1 {
			// Incomplete string/comment — need more bytes.
			return 0, false, nil
		}
		if j != i {
			i = j
			continue
		}
		if input[i] == '@' && i+len("@dataset") <= len(input) &&
			string(input[i:i+len("@dataset")]) == "@dataset" {
			// Confirm the next char (if present) isn't an ident-part.
			after := i + len("@dataset")
			if after == len(input) {
				// Could be `@dataset` followed by more bytes we haven't
				// seen yet — be conservative.
				return 0, false, nil
			}
			if !isIdentPart(input[after]) {
				return i, true, nil
			}
		}
		i++
	}
	return 0, false, nil
}

// findNextChar returns the offset of the next occurrence of `ch`
// outside strings/comments, starting at startFrom.
func findNextChar(input []byte, startFrom int, ch byte) (int, bool, error) {
	i := startFrom
	for i < len(input) {
		j, err := skipStringOrComment(input, i)
		if err != nil {
			return 0, false, err
		}
		if j == -1 {
			return 0, false, nil
		}
		if j != i {
			i = j
			continue
		}
		if input[i] == ch {
			return i, true, nil
		}
		i++
	}
	return 0, false, nil
}

// skipStringOrComment returns the index past a string / bytes literal
// / comment starting at i, if i is positioned at the opener. Returns
// i unchanged if the byte at i is not an opener. Returns -1 if the
// construct is incomplete (caller should treat as "need more bytes").
// Returns a non-nil error, with the index of the offending byte, if the
// construct is malformed in a way that can't be fixed by more bytes (a
// bytes literal broken by a newline).
func skipStringOrComment(input []byte, i int) (int, error) {
	if i >= len(input) {
		return i, nil
	}
	ch := input[i]
	switch {
	case ch == '"':
		// Triple-quoted vs. single-quoted.
		if i+2 < len(input) && input[i+1] == '"' && input[i+2] == '"' {
			return skipTripleString(input, i)
		}
		return skipSimpleString(input, i)
	case ch == 'b' && i+1 < len(input) && input[i+1] == '"' && (i == 0 || !isIdentPart(input[i-1])):
		// The lexer reads b" as a bytes literal only at a token start;
		// inside an identifier (`Ab"`) the quote opens a string.
		return skipBytesLiteral(input, i)
	case ch == '#':
		return skipLineComment(input, i+1), nil
	case ch == '/' && i+1 < len(input) && input[i+1] == '/':
		return skipLineComment(input, i+2), nil
	case ch == '/' && i+1 < len(input) && input[i+1] == '*':
		return skipBlockComment(input, i+2)
	}
	return i, nil
}

func skipSimpleString(input []byte, i int) (int, error) {
	// i is at the opening `"`.
	j := i + 1
	for j < len(input) {
		switch input[j] {
		case '\\':
			if j+1 >= len(input) {
				return -1, nil // need more bytes
			}
			j += 2
		case '"':
			return j + 1, nil
		default:
			// A raw LF does not end the scan: the strict lexer keeps it
			// in the literal, and the parser, not this scan, decides
			// whether a row is valid.
			j++
		}
	}
	return -1, nil
}

func skipTripleString(input []byte, i int) (int, error) {
	j := i + 3
	for j+2 < len(input) {
		if input[j] == '"' && input[j+1] == '"' && input[j+2] == '"' {
			return j + 3, nil
		}
		j++
	}
	return -1, nil
}

func skipBytesLiteral(input []byte, i int) (int, error) {
	j := i + 2 // past `b"`
	for j < len(input) {
		switch input[j] {
		case '"':
			return j + 1, nil
		case '\n':
			return j, errors.New("pxf: unterminated bytes literal")
		default:
			j++
		}
	}
	return -1, nil
}

func skipLineComment(input []byte, from int) int {
	j := from
	for j < len(input) && input[j] != '\n' {
		j++
	}
	return j
}

func skipBlockComment(input []byte, from int) (int, error) {
	j := from
	for j+1 < len(input) {
		if input[j] == '*' && input[j+1] == '/' {
			return j + 2, nil
		}
		j++
	}
	return -1, nil
}

// findMatchingParenSafe finds the index of the `)` matching the `(`
// at openIdx. Returns:
//   - (index, true, nil) on success.
//   - (0, false, nil) if the matching paren isn't in the buffer yet
//     (caller should pull more bytes).
//   - (0, false, err) on a malformed string / bytes / comment inside
//     the parens (unrecoverable — surfaces to the caller).
//
// String / bytes-literal / comment aware.
func findMatchingParenSafe(input []byte, openIdx int) (int, bool, error) {
	depth := 1
	i := openIdx + 1
	for i < len(input) {
		// Try to skip past any string/bytes/comment construct.
		j, err := skipStringOrComment(input, i)
		if err != nil {
			return 0, false, err
		}
		if j == -1 {
			// Incomplete construct — pull more bytes.
			return 0, false, nil
		}
		if j != i {
			i = j
			continue
		}
		switch input[i] {
		case '(':
			depth++
			i++
		case ')':
			depth--
			if depth == 0 {
				return i, true, nil
			}
			i++
		default:
			i++
		}
	}
	return 0, false, nil
}

// rowScan classifies what findNextRow found at the head of the input.
type rowScan int

const (
	// scanNeedMore: the input ends before the next row closes. start is
	// where the unfinished construct begins, or -1 if only whitespace
	// and comments were seen.
	scanNeedMore rowScan = iota
	// scanRow: input[start:end+1] is a complete `( ... )` row.
	scanRow
	// scanBad: the row at start can never close well (a '(' inside it,
	// or a bytes literal broken across a line, at end). Parsing
	// input[start:end+1] fails where Parse fails: every token in it is
	// complete, so a cut at the buffer's end cannot change the error.
	scanBad
	// scanEnd: a directive starts at start; the row sequence is over.
	scanEnd
	// scanNotRow: start holds something that is neither a row nor a
	// directive, which Parse rejects as a top-level entry.
	scanNotRow
)

// findNextRow finds the next `( ... )` row in input, skipping leading
// whitespace and comments. It is a byte-level scan, aware of strings,
// bytes literals and comments; the row itself is parsed by the caller.
func findNextRow(input []byte) (kind rowScan, start, end int) {
	i := 0
	for i < len(input) {
		ch := input[i]
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
			i++
			continue
		}
		if ch == '#' || (ch == '/' && i+1 < len(input) && (input[i+1] == '/' || input[i+1] == '*')) {
			j, _ := skipStringOrComment(input, i)
			if j == -1 {
				return scanNeedMore, i, 0
			}
			i = j
			continue
		}
		if ch == '/' && i+1 == len(input) {
			return scanNeedMore, i, 0 // could be a comment opener
		}
		break
	}
	if i >= len(input) {
		return scanNeedMore, -1, 0
	}
	switch input[i] {
	case '(':
	case '@':
		// A directive name follows its '@' directly (lexDirective). A
		// bare '@' is no directive; Parse rejects it as a top-level
		// entry.
		if i+1 == len(input) {
			return scanNeedMore, i, 0
		}
		if !isIdentPart(input[i+1]) {
			return scanNotRow, i, 0
		}
		return scanEnd, i, 0
	default:
		return scanNotRow, i, 0
	}
	for j := i + 1; j < len(input); {
		k, err := skipStringOrComment(input, j)
		if err != nil {
			return scanBad, i, k
		}
		if k == -1 {
			return scanNeedMore, i, 0
		}
		if k != j {
			j = k
			continue
		}
		switch input[j] {
		case ')':
			return scanRow, i, j
		case '(':
			// No PXF value holds a '(' outside a string, list and block
			// cells included, so this row is malformed however the input
			// continues. The
			// '(' may sit inside a malformed token, though (`0000-(0`
			// lexes as one bad timestamp), so the error is only Parse's
			// once that token is complete: end at the byte before the
			// next one that ends every token.
			// (The set is lexTimestamp's, the longest-running token.)
			for k := j + 1; k < len(input); k++ {
				switch input[k] {
				case ' ', '\t', '\r', '\n', ',', ')', ']', '}', '#':
					return scanBad, i, k - 1
				case '/':
					if k+1 == len(input) {
						return scanNeedMore, i, 0
					}
					if input[k+1] == '/' || input[k+1] == '*' {
						return scanBad, i, k - 1
					}
				}
			}
			return scanNeedMore, i, 0
		}
		j++
	}
	return scanNeedMore, i, 0
}
