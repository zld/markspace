# Vendor modifications

## github.com/bluenviron/gortsplib/v4 v4.12.2

### `server_session_format.go` — bypass the server-side UDP reorder queue

Upstream gortsplib runs every UDP RTP packet through an internal
`rtpreorderer.Reorderer` (64-packet window) before invoking the
`OnPacketRTP` callback. That queue both re-sorts out-of-order packets and
silently drops duplicate packets, so an application-level receiver can never
observe them.

This ingest tool must account for sequence gaps, duplicates and
out-of-order delivery itself (see `internal/rtp/reorder.go`). The vendored
`readPacketRTPUDP` was therefore changed to deliver packets in raw arrival
order. Loss *detection* is retained through `rtplossdetector.LossDetector`,
which is purely observational and never drops a packet. No other file or
behavior is modified.

Re-apply this hunk whenever `go mod vendor` is re-run.
