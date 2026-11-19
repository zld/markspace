# frameaudit

Local command-line diagnostics for decoded video frame order, frame type
distribution and timestamp repair. Pure local processing, no GUI, no network.

## Install

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install -e '.[test]'
```

## Generate a synthetic fixture (B-frames plus injected timestamp faults)

```sh
.venv/bin/python scripts/make_fixtures.py --output fixtures/bframes.mkv
```

## Run an audit

```sh
.venv/bin/python -m frameaudit.cli fixtures/bframes.mkv \
    --fps 30000/1001 --jump-threshold 2.0 --json-out /tmp/frameaudit.json
```

## Tests

```sh
.venv/bin/python -m pytest
```

Exit codes: `0` success, `1` decode/output failure, `2` invalid argument or
unsupported input.
