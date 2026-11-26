module regress

go 1.23.12

// Pinned per project specification. pion/rtp v1.10.6 declares go 1.24 in its
// own go.mod but only uses Go 1.22 language features; the local replace below
// keeps the v1.10.6 source while lowering the toolchain directive to 1.23.
require (
	github.com/abema/go-mp4 v1.7.3
	github.com/pion/rtp v1.10.6
)

replace github.com/pion/rtp => ./third_party/rtp
