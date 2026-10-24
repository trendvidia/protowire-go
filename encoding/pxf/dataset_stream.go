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
	"slices"

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
	finished bool         // Next() has returned io.EOF
	err      error        // sticky error
	lim      limits       // the options' limits; lim.maxMessageSize caps pending while a row boundary is sought
	base     Position     // document position of pending[0], so rows and errors carry Parse's positions
	resolver TypeResolver // the options' TypeResolver, for Scan: an Any cell may name its type (protowire#354)

	// The header and row scans resume where the previous pull left
	// them, so a header or a row is lexed once however many reads it
	// arrives in (#148). scanPos is the start of the first token not yet
	// known to be complete; rowStart is the offset of the current row's
	// '(' , or -1 between rows. Both are offsets into pending.
	scanPos  int
	rowStart int
	hdr      headerScan // the header scan's state, until the header ends

	// While a token runs past the buffer's end (waiting), the scan does
	// not lex it again until bytes arrive that could end it
	// (mayEndToken): waitAt is the token's start and waitFrom the buffer
	// length already checked. Without this, one long literal costs a
	// re-lex of the whole literal on every read.
	waiting  bool
	waitAt   int
	waitFrom int

	// scanned counts the bytes the header and row scans have lexed, for
	// the tests that hold them linear in the input.
	scanned int

	lx lexer // the scan's lexer, reset for each token (nextToken)

	chained bool // built on another reader's Tail, so it starts mid-document

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
// MaxMessageSize, as Unmarshal would bind the body the row stands for,
// and with the options' TypeResolver, which an Any cell opening with
// `@type = "..."` needs.
func (o UnmarshalOptions) NewDatasetReader(r io.Reader) (*DatasetReader, error) {
	tr := &DatasetReader{src: r, lim: o.limits(), base: Position{Line: 1, Column: 1}, rowStart: -1, resolver: o.TypeResolver}
	if t, ok := r.(*datasetTail); ok {
		tr.base = t.at
		tr.chained = true
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
			// The newlines and comments before the directive stay in the
			// buffer, so Tail carries them: Parse gives them to that
			// directive as its LeadingComments (#149).
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
// spans many reads is still lexed in linear time: Next calls it only
// between rows (rowStart is -1), where no token is waiting, since every
// earlier Next returned a whole row or a terminal state.
func (tr *DatasetReader) tryWholeRow() (row DatasetRow, end int, ok bool) {
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
	tr.pending = slices.Grow(tr.pending, n)
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
		if bad != nil {
			// The lexer rejected a token, or an @proto body can never
			// close, before the header ended. Every token before it is
			// complete, so Parse fails at or before it (the whole buffer,
			// for the reason scanBad gives).
			if _, perr := tr.parseHeader(tr.pending); perr != nil {
				return perr
			}
			// Unreachable: Parse reads what the scan read.
			return errorf(tr.posAt(bad.Pos.Offset), "%s", bad.Value)
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
			if tr.chained && len(doc.LeadingComments) > 0 && len(doc.Directives) > 0 &&
				doc.Directives[0].Pos.Offset < ds.Pos.Offset {
				// A reader on Tail starts mid-document, where the comments
				// before a directive are its own, as Parse files them; at
				// a document's start they are the document's.
				doc.Directives[0].LeadingComments = append(doc.LeadingComments, doc.Directives[0].LeadingComments...)
			}
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
// offset just past it, or ok=false when more input could still change
// it: the lexer looked for a byte at or past the buffer's end to decide
// it (an identifier, a number, a string or a comment without its end,
// a '/' or a '"' whose next bytes are not read yet), or there is no
// token yet. Offsets are into pending. This is the lexer Parse uses,
// and it says itself when it ran out of input, so the scan and Parse
// can never disagree about where a token ends (#149).
func (tr *DatasetReader) nextToken(from int) (tok Token, end int, ok bool) {
	// One lexer value, reset per token, so the scan allocates nothing
	// per token beyond what the token itself holds.
	tr.lx = lexer{input: tr.pending[from:], line: 1, col: 1, base: from, maxBytesLiteral: tr.lim.maxBytesLiteral, noValue: true}
	lx := &tr.lx
	tok = lx.Next()
	end = from + lx.pos
	tr.scanned += end - from
	return tok, end, tr.srcEOF || (tok.Kind != EOF && !lx.sawEnd)
}

// resume reports whether a scan may lex on: no token is waiting, the
// source has ended, or the bytes read since the waiting token was last
// lexed could end it.
func (tr *DatasetReader) resume() bool {
	if tr.waiting && !tr.srcEOF && !tr.mayEndToken() {
		tr.waitFrom = len(tr.pending)
		return false
	}
	tr.waiting = false
	return true
}

// wait records tok, which nextToken could not decide, as the waiting
// token. The scan resumes at its start: the spaces before it are
// decided, and lexing them again would cost as much as the token.
func (tr *DatasetReader) wait(tok Token) {
	tr.waiting, tr.waitAt, tr.waitFrom = true, tok.Pos.Offset, len(tr.pending)
	tr.scanPos = tok.Pos.Offset
}

// scanRow finds the next row with the lexer, resuming at scanPos: a
// token known complete is never lexed again, so a row costs time linear
// in its size however many reads it arrives in (#148). Between rows it
// skips newlines and comments; '(' starts a row, a directive ends the
// sequence, and anything else is not a row. Inside a row it stops at
// the ')' that closes it, at a '(' (no value holds one), or at a token
// the lexer rejects.
func (tr *DatasetReader) scanRow() (kind rowScan, start, end int) {
	if !tr.resume() {
		return scanNeedMore, tr.rowStart, 0
	}
	for {
		tok, tokEnd, ok := tr.nextToken(tr.scanPos)
		if !ok {
			tr.wait(tok)
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

// shortWait is the length under which a waiting token is lexed again
// whatever bytes arrive. What may still change a token this short is a
// byte or two of the lexer's lookahead (`""` opening `"""`, `b` opening
// `b"`, `/` opening a comment, `-in` reading `-inf`, a multi-byte rune
// cut in half, no token at all yet), and lexing a few bytes again costs
// nothing.
const shortWait = 8

// mayEndToken reports whether the bytes read since the waiting token was
// last lexed could end it: a closing quote for a string (one not
// escaped by an odd run of backslashes) or a newline, which a simple
// string cannot hold, `"""` for a triple-quoted one, a quote or newline
// for a bytes literal, or enough characters that the lexer refuses it
// (MaxBytesLiteralLength), a newline for a line comment, `*/` for a
// block comment, a byte that ends a run (endsRun) or a comment opener
// for a timestamp, and for any other token a byte that cannot continue
// an identifier, number or duration. Each of those does end the token,
// so a token is lexed again only once it is short or decided, and the
// scan stays linear. It only decides when the lexer looks again, never
// what the token is; a byte that ends a row, ')', always counts, so a
// complete row is never left waiting for input.
func (tr *DatasetReader) mayEndToken() bool {
	buf, at, from := tr.pending, tr.waitAt, tr.waitFrom
	if from-at < shortWait {
		return true
	}
	tok, fresh := buf[at:], buf[from:]
	switch {
	case bytes.HasPrefix(tok, []byte(`"""`)):
		return bytes.Contains(buf[max(from-2, at+3):], []byte(`"""`))
	case tok[0] == '"':
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
		// The lexer checks the cap before each character it reads past
		// the opening quote, the last one included.
		return bytes.ContainsAny(fresh, "\"\n") || bytesLiteralOver(len(buf)-1-(at+2), tr.lim.maxBytesLiteral)
	case tok[0] == '#', bytes.HasPrefix(tok, []byte("//")):
		return bytes.IndexByte(fresh, '\n') >= 0
	case bytes.HasPrefix(tok, []byte("/*")):
		return bytes.Contains(buf[max(from-1, at+2):], []byte("*/"))
	case isDigit(tok[0]) && isDigit(tok[1]) && isDigit(tok[2]) && isDigit(tok[3]) && tok[4] == '-':
		// A timestamp (lexNumber): it runs to a byte that ends a run, or
		// to `//` or `/*`, whose '/' may be the last byte checked before.
		for i := max(from-1, at); i < len(buf); i++ {
			if endsRun(buf[i]) || buf[i] == '/' && i+1 < len(buf) && (buf[i+1] == '/' || buf[i+1] == '*') {
				return true
			}
		}
		return false
	}
	for _, ch := range fresh {
		if !(isIdentPart(ch) || ch == '+' || ch == '-' || ch >= 0x80) {
			return true
		}
	}
	return false
}

// headerScan is where scanHeader stands in the header: whether it has
// read the @dataset token, and how far into an @proto directive whose
// body is raw protobuf source, which Parse skips with braceMatch rather
// than lexing it.
type headerScan struct {
	inDataset bool
	proto     int        // 1 after @proto, 2 after its type name, else 0
	inBody    bool       // body matches an @proto body's '{' ... '}'
	body      braceMatch // the body matched so far, resumed at scanPos
}

// scanHeader finds the end of the first `@dataset TYPE ( cols )` header
// with the lexer: the offset just past the ')' that closes the column
// list, the first ')' after an @dataset token. It returns found=false
// when the input ends first. bad is the token the lexer rejected, or an
// @proto body that can never close, before the header ended, or nil.
// Like scanRow it resumes at scanPos, so a header costs time linear in
// its size however many reads it arrives in.
func (tr *DatasetReader) scanHeader() (end int, found bool, bad *Token) {
	h := &tr.hdr
	if !h.inBody && !tr.resume() {
		return 0, false, nil
	}
	for {
		if h.inBody {
			i, st := h.body.scan(tr.pending, tr.scanPos)
			tr.scanned += i - tr.scanPos
			tr.scanPos = i
			switch st {
			case braceOpen:
				return 0, false, nil
			case braceFailed:
				return 0, false, &Token{Kind: ILLEGAL, Value: "@proto: unmatched '{'", Pos: Position{Offset: i}}
			}
			h.inBody = false
			tr.scanPos++ // the closing '}'
			continue
		}
		tok, tokEnd, ok := tr.nextToken(tr.scanPos)
		if !ok {
			tr.wait(tok)
			return 0, false, nil
		}
		switch {
		case tok.Kind == EOF:
			// Only at the end of the source (nextToken).
			return 0, false, nil
		case tok.Kind == ILLEGAL:
			return 0, false, &tok
		case tok.Kind == NEWLINE || tok.Kind == COMMENT:
			// Parse skips these between @proto, its name and '{'.
		case h.inDataset:
			if tok.Kind == RPAREN {
				tr.scanPos = tokEnd
				return tokEnd, true, nil
			}
		case tok.Kind == AT_DATASET:
			h.inDataset, h.proto = true, 0
		case tok.Kind == AT_PROTO:
			h.proto = 1
		case tok.Kind == IDENT && h.proto == 1:
			h.proto = 2
		case tok.Kind == LBRACE && h.proto > 0:
			h.proto, h.inBody, h.body = 0, true, braceMatch{depth: 1}
		default:
			h.proto = 0
		}
		tr.scanPos = tokEnd
	}
}
