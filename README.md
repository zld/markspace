# lutctl

Local 3D LUT rebaking tool for colour-asset pipelines. It parses Adobe
`.cube` LUTs, rebakes them across colour spaces and grid sizes with an
explicit decode → convert → sample → encode pipeline, and quantifies the
result against probe grids and P6 patch images.

Pure C++17, standard library only. No network, GUI or third-party runtime
dependencies.

## Build

```sh
brew install cmake   # if needed
cmake -S . -B /tmp/lut-build -DCMAKE_BUILD_TYPE=Release
cmake --build /tmp/lut-build -j
ctest --test-dir /tmp/lut-build --output-on-failure
```

## Commands

| Command | Purpose |
| --- | --- |
| `fixture-gen` | Generate a smooth non-identity 25³ `.cube` and a 64×64 P6 patch image. |
| `bake` | Rebake a source LUT to a target space/grid; compare direct vs baked sampling on a probe grid; collect patch histograms. |
| `probe` | Grade a single `R,G,B` sample through a LUT via the shared trilinear kernel. |
| `render` | Grade a P6 image (8- or 16-bit) through a LUT and write a P6 output. |

Common options: `--domain-policy clamp|error` (default `clamp`),
`--max-error` for the bake deviation gate, `--tile-rows` for render
scheduling (does not change pixel results).

### Example

```sh
lutctl fixture-gen --out /tmp/lut-demo --patch-space display-p3
lutctl bake --cube /tmp/lut-demo/source.cube --source-space srgb \
    --target-space display-p3 --size 33 --probe-grid 17 \
    --patches /tmp/lut-demo/patches.ppm --out /tmp/lut-demo/baked.cube \
    --max-error 0.02
lutctl probe --cube /tmp/lut-demo/baked.cube --space display-p3 \
    --rgb 0.20,0.40,0.60
lutctl render --cube /tmp/lut-demo/baked.cube --space display-p3 \
    --input /tmp/lut-demo/patches.ppm --output /tmp/lut-demo/graded.ppm \
    --tile-rows 16
```

`scripts/demo.sh` runs the full flow end to end.

## Colour pipeline

For every encoded input sample the bake executes, in order:

1. **Decode** the source transfer function (sRGB / Display-P3 / Rec.709).
2. **Convert** linear source RGB to linear target RGB through D65
   RGB↔XYZ matrices built from the space primaries (singular matrices are
   rejected).
3. **Encode** to the target transfer to get the LUT sample coordinate.
4. **Sample** the source LUT with the shared trilinear kernel.

The probe and image render paths call the same `Interpolator::sample`
kernel, so identical inputs produce identical outputs.

## Reported fields

`bake` prints `grid_size`, `probe_count`, per-channel
`max_deviation_r/g/b`, and `hist_before_*` / `hist_after_*` as 16
integers per channel (each channel totals the pixel count). Out-of-domain
input pixels and clipped output pixels are counted separately, both per
pixel (a pixel with several offending channels counts once).

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success. |
| 2 | Bad command-line arguments. |
| 3 | Invalid LUT or colour-space math (parse errors include line numbers). |
| 4 | File or image I/O failure. |
| 5 | Bake deviation exceeded `--max-error`; no output file is written. |

All outputs are written via a sibling temporary file and atomically
renamed, so a failure can never leave a partial target that looks complete.

## Layout

```
include/lut/   public headers (model, colour space, parser, kernel, ...)
src/           pipeline implementation and the lutctl CLI
tests/         CTest unit and end-to-end suites
scripts/       demo driver
```
