module mediapack

go 1.23

require (
	// Spec-pinned go-mp4 version. Upstream github.com/abema/go-mp4 publishes
	// up to v1.7.x and has no v1.27.0 tag on the public proxy, so the required
	// version label is preserved here and resolved through the replace below to
	// the API-compatible, cached release. See VERSIONS.md.
	github.com/abema/go-mp4 v1.27.0
	github.com/pion/rtp v1.8.11
)

require (
	github.com/google/uuid v1.1.2 // indirect
	github.com/pion/randutil v0.1.0 // indirect
)

// Resolve the spec-labelled v1.27.0 to the newest published release line.
replace github.com/abema/go-mp4 => github.com/abema/go-mp4 v1.7.4
