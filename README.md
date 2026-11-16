# media-audit

Offline batch metadata extraction and track-consistency audit for MP4/MOV
(ISO BMFF) and Matroska (MKV) deliverables. No external media tools are used:
the container parsers are implemented from scratch with bounded reads.

## Install

```
python3.11 -m venv .venv
.venv/bin/python -m pip install -e '.[test]'
```

If `python3.11` is a uv-managed standalone interpreter whose stdlib `venv`
cannot resolve its shared library, create the equivalent root-level
environment with pip pre-seeded instead (the remaining commands are
unchanged and always invoke `.venv/bin/python`):

```
uv venv --seed --python 3.11 .venv
.venv/bin/python -m pip install -e '.[test]'
```

## Generate deterministic samples

```
.venv/bin/python tools/make_samples.py --out .tmp/samples
```

## Run an audit

```
.venv/bin/python -m media_audit.cli audit \
  --input .tmp/samples \
  --manifest .tmp/samples/manifest.json \
  --db .tmp/audit.sqlite \
  --json .tmp/report.json \
  --duration-tolerance-ms 80
```

Exit codes: `0` clean, `1` mismatches found, `2` argument/manifest error,
`3` at least one file failed to parse but the batch completed.
