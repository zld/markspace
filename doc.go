// Package mediacheck is the module root. The kernel lives in three packages:
//
//	github.com/zld/media-check/media       MP4/MOV probing, mapping, remux, index
//	github.com/zld/media-check/transport   RTP parse, reorder, bandwidth control
//	github.com/zld/media-check/pipeline    bounded pool, deterministic merge, run
//
// The acceptance adapter is cmd/mediacheck.
package mediacheck
