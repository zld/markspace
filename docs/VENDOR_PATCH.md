# Vendored gortsplib modification

All third-party source lives under `vendor/`. This document lives outside
`vendor/` on purpose: `go mod vendor` deletes every file in `vendor/` that is
not part of a dependency module, so documentation there would not survive a
re-vendor.

## Module

`github.com/bluenviron/gortsplib/v4` v4.12.2, file
`server_session_format.go`, function `readPacketRTPUDP`.

## Why it is needed

Upstream gortsplib runs every UDP RTP packet through an internal
`rtpreorderer.Reorderer` (64-packet window) before invoking the
`OnPacketRTP` callback. That queue both re-sorts out-of-order packets and
silently drops duplicate packets, so an application-level receiver can never
observe them. This tool must account for sequence gaps, duplicates and
out-of-order delivery itself (`internal/rtp/reorder.go`).

A socket-level interception is not possible without the patch: gortsplib
type-asserts the bound listener to a concrete `*net.UDPConn`, so a wrapping
`net.PacketConn` is rejected at startup.

## What the patch changes

`readPacketRTPUDP` calls `handlePacketRTP` directly, delivering packets in
raw arrival order with nothing dropped and no library loss reporting. The
application's own wraparound-aware reorder buffer replaces both functions.
No other file or behavior is modified.

## Keeping it applied

A bare `go mod vendor` refreshes the tree from the pristine module and wipes
the hunk. Two safeguards exist:

1. `make vendor` runs `go mod vendor` and immediately re-applies the patch.
   Manually, the idempotent command is:

   ```sh
   go run -mod=vendor ./tools/apply-vendor-patch
   ```

2. `TestVendorPatchPresent` fails with the same remediation message if the
   patch is ever missing, so the condition can never fail silently.
