module regress

go 1.23.12

// Pinned per project specification. pion/rtp v1.10.6 declares go 1.24 in its
// own go.mod but only uses Go 1.22 language features; the local replace below
// keeps the v1.10.6 source while lowering the toolchain directive to 1.23.
require (
	github.com/abema/go-mp4 v1.7.3
	github.com/pion/rtp v1.10.6
)

require github.com/sunfish-shogi/bufseekio v0.0.0-20210207115823-a4185644b365

require (
	github.com/google/uuid v1.1.2 // indirect
	github.com/pion/randutil v0.1.0 // indirect
)

replace github.com/pion/rtp => ./third_party/rtp
