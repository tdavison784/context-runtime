package directive

import "errors"

// ErrNotImplemented marks the temporary parser skeleton. The parser worker
// replaces Parse's body; callers must fail closed while this stub is present.
var ErrNotImplemented = errors.New("directive parser not implemented")

// Parse interprets one immutable text part under the supplied source gate and
// limits. It preserves original byte offsets across BOM, CRLF, lone CR, and
// invalid UTF-8; keyword matching is ASCII-only. Parser implementation belongs
// to p2-parser. This skeleton deliberately performs no parsing.
func Parse(input []byte, opts Options) Result {
	return Result{Err: ErrNotImplemented}
}
