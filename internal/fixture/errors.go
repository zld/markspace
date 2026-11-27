package fixture

import merr "mediatool/internal/errors"

// errTruncated signals that a capture file ended inside a framed packet.
var errTruncated = merr.New(merr.CodeTruncated, "capture ends inside a packet frame")

// IsTruncated reports whether err is the fixture truncation sentinel.
func IsTruncated(err error) bool {
	return merr.AsCode(err) == merr.CodeTruncated
}
