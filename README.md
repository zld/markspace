# mediakey

Offline media packaging and license validation for a local build machine.
Segments are generated locally, encrypted with rotating AES-256-GCM keys,
described by a deterministic manifest and released under an HMAC-signed local
license. Validation reports one stable reason code per segment so a build
script can tell a corrupt segment from a wrong key version or an invalid
license.

Everything runs in one process with the standard library only. No network
calls, no third-party modules, no key material written to disk or to output.

## Requirements

- Go 1.22 or newer (developed and tested on macOS arm64; also builds for
  darwin/amd64)
- A terminal. There is no GUI, browser or media-device dependency.

## Quick start

```sh
go test ./...
go run ./cmd/mediakey demo --segments 12 --rotate-every 3 \
  --license-ttl 60s --advance-clock 75s --replay-check
scripts/demo.sh
```

The demo prints a key=value summary and a JSON report. Exit codes:

| code | meaning |
| ---- | ------- |
| 0 | validation passed |
| 2 | validation reported failures |
| 1 | bad argument or packaging/build error |

## Flags

| flag | default | meaning |
| ---- | ------- | ------- |
| `--segments N` | 12 | sample segment count, maximum 10000 |
| `--rotate-every N` | 3 | rotate the key every N segments |
| `--license-ttl D` | 1m | license validity window |
| `--advance-clock D` | 0s | advance the clock after validation to probe expiry |
| `--replay-check` | off | present one serial concurrently to probe replay |
| `--corrupt-segment N` | off | flip a byte in segment N (`AUTH_FAILED`) |
| `--drop-segment N` | off | withhold segment N (`SEGMENT_MISSING`) |
| `--seed N` | 20260420 | deterministic content, key, nonce and secret stream |
| `--asset-id ID` | demo-asset | asset identifier bound by the license |
| `--segment-size N` | 4096 | nominal plaintext size per segment |
| `--replay-attempts N` | 4 | concurrent replay presentations |
| `--production` | off | use the system clock and crypto/rand |
| `--json` | off | print the JSON report only |

Demo runs are seeded by default so rotation and expiry boundaries reproduce
byte for byte. `--production` switches to the system clock and `crypto/rand`,
which is the mode a real deployment would use.

## Report shape

Top-level fields are emitted in a fixed order, slices are sorted, and no maps
are used, so identical results always serialize identically.

```json
{
  "asset_id": "demo-asset",
  "verdict": "ok",
  "rotation_count": 3,
  "segment_count": 12,
  "ok_count": 12,
  "failed_count": 0,
  "license": { "serial": "lic-…", "key_min": 1, "key_max": 4, "status": "ok", "reason": "NONE" },
  "manifest_check": { "declared_segment_count": 12, "present_segment_count": 12, "status": "ok", "reason": "NONE" },
  "rotations": [ { "rotation": 1, "from_key_id": "k1", "to_key_id": "k2", "at_segment": 3 } ],
  "keys": [ { "key_id": "k1", "version": 1, "active": false, "expired_at_validation": false } ],
  "segments": [ { "segment_index": 0, "key_id": "k1", "status": "ok", "reason": "NONE" } ],
  "replay": { "checked": true, "successes": 1, "replays": 3, "rejected": true },
  "boundary_check": { "checked": true, "license_reason": "LICENSE_EXPIRED",
                      "key_probe_ok_count": 0, "key_probe_failed_count": 12 }
}
```

## Reason codes

| code | stage | meaning |
| ---- | ----- | ------- |
| `NONE` | any | success |
| `DUPLICATE_NONCE` | packaging / manifest | nonce reused; packaging aborts, validation flags every owner |
| `LICENSE_MISSING_FIELD` | license | a required license field is absent |
| `LICENSE_SIGNATURE` | license | HMAC does not verify |
| `LICENSE_ASSET_MISMATCH` | license | license is scoped to another asset |
| `LICENSE_EXPIRED` | license | validation time is at or past expiry |
| `LICENSE_REPLAY` | replay | serial already consumed |
| `KEY_UNKNOWN` | key | manifest names a key version this process never issued |
| `KEY_EXPIRED` | key | version exists but its validity window has passed; no fallback |
| `KEY_OUT_OF_RANGE` | key | version is outside the license's allowed range |
| `KEY_ID_MISMATCH` | key | key id and recorded version disagree |
| `SEGMENT_MISSING` | segment | no ciphertext delivered for a manifest index |
| `SEGMENT_UNEXPECTED` | segment | ciphertext delivered for an index not in the manifest |
| `SEGMENT_DUPLICATE` | segment | two manifest entries share an index |
| `SEGMENT_ORDER` | segment | manifest positions are not ascending |
| `NONCE_INVALID` | segment | manifest nonce is malformed |
| `AUTH_FAILED` | segment | GCM authentication failed (tamper, wrong key or nonce) |
| `DIGEST_MISMATCH` | segment | plaintext or ciphertext digest disagrees with the manifest |
| `SEGMENT_COUNT_MISMATCH` | manifest | declared count differs from listed segments |
| `INVALID_INPUT` / `BUILD_ERROR` | cli | the run could not start |

Classification order per segment is: manifest entry, nonce, key resolution,
GCM authentication, then digests. Authentication is checked before digests so
a flipped byte is reported as `AUTH_FAILED` rather than a digest mismatch. A
single failing segment never stops the rest.

## Security scope

This is a local demonstration pipeline, not a production key-management
system. Keys and the license signing secret live only in process memory and
are never serialized; production mode uses `crypto/rand` and the system clock.
It does not integrate with a real authorization service and makes no claim
about protecting keys against a host-level attacker.

## Layout

```
cmd/mediakey/        CLI flag parsing, exit codes, output
internal/asset/      manifest, segment metadata, stable encoding
internal/crypto/     AES-256-GCM seal/open with asset+index AAD
internal/issuer/     in-process key versions, rotation and expiry history
internal/license/    HMAC-SHA256 license issue/verify
internal/replay/     atomic serial registration
internal/pipeline/   packaging, validation classification, run orchestration
internal/report/     stable JSON and key=value summary
internal/reason/     stable reason codes
testdata/            deterministic sample generator and entropy streams
scripts/demo.sh      end-to-end walkthrough
```
