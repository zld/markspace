module example.com/assetcheck

go 1.23

// Upstream never published a v1.0.4 tag (tags jump from v1.0.0 to v1.1.0).
// The requested pin is kept in require and resolved to the closest available
// v1.0.x release, which exposes the identical container/time-base API used by
// this project (BoxInfo, mvhd, tkhd, mdhd). See README.md.
replace github.com/abema/go-mp4 v1.0.4 => github.com/abema/go-mp4 v1.0.0
