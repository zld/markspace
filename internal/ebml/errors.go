package ebml

import "fmt"

// Hard safety limits. Inputs exceeding any of these are rejected rather than
// trusted, so a corrupt header cannot force unbounded allocation or recursion.
const (
	MaxElementSize = 256 << 20 // 256 MiB per single element payload
	MaxDepth       = 16        // nested element depth
	MaxIDLength    = 4         // Matroska element IDs are at most VINT_WIDTH 4
)

// ErrorKind classifies an EBML failure so callers can distinguish corrupt
// input from internal/resource failures without string matching.
type ErrorKind int

const (
	KindOK        ErrorKind = iota
	KindTruncated           // input ended before an element (header or payload) completed
	KindOversize            // declared size/depth exceeds a configured or hard limit
	KindBadVINT             // malformed variable-size integer
)

// Error carries a machine-readable kind plus the byte offset and, when
// available, the element ID being processed when the failure occurred.
type Error struct {
	Kind   ErrorKind
	Offset int64
	ElemID uint32
	Msg    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("ebml: %s at offset %d (element 0x%X)", e.Msg, e.Offset, e.ElemID)
}

// Is supports errors.Is on Kind, e.g. errors.Is(err, &Error{Kind: KindTruncated}).
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Kind == e.Kind
}

func fail(kind ErrorKind, offset int64, id uint32, format string, args ...any) error {
	return &Error{Kind: kind, Offset: offset, ElemID: id, Msg: fmt.Sprintf(format, args...)}
}

// frame is one open element on the descent stack. end==-1 marks an
// unknown-length element whose boundary is only known when its parent ends.
type frame struct {
	id  uint32
	end int64 // absolute byte offset of the payload end, or -1
}
