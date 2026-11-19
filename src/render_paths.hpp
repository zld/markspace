#pragma once

#include <algorithm>
#include <cstddef>
#include <cstdint>
#include <functional>
#include <limits>
#include <memory>
#include <string>

#include "color_space.hpp"
#include "lut_parser.hpp"
#include "types.hpp"

namespace lutcheck {

inline constexpr int kMaxThreads = 32;

struct RenderOptions {
  Mat3 input_to_lut = Mat3::identity();
  // Samples whose LUT-space value falls outside this range are still
  // interpolated after clamping; keeping it explicit documents that the
  // pipeline never silently rescales.
  double min_value = 0.0;
  double max_value = 1.0;
};

// Applies the conversion matrix and the LUT to one sample. Both render paths
// funnel through here so their arithmetic is identical by construction.
Vec3 transform_sample(const Lut3D& lut, const RenderOptions& options,
                      const Vec3& input);

Image render_scalar(const Image& input, const Lut3D& lut,
                    const RenderOptions& options);

using ChunkWork = std::function<void(std::size_t, std::size_t)>;

struct ParallelResult {
  Image image;
  int threads_used = 0;
};

// Runs `body(begin, end)` over contiguous chunks of `total` items. This is
// internal plumbing exposed for tests; prefer render_parallel. Chunk
// boundaries are derived only from `total` and `chunk_pixels`, so results are
// independent of thread count. Any exception thrown by `body` is captured,
// all workers are joined, and the first exception is rethrown. Returns the
// number of workers actually started.
std::size_t run_chunked(std::size_t total, int thread_count,
                        std::size_t chunk_pixels, const ChunkWork& body);

// Splits the image into fixed-size contiguous chunks, one per worker, and
// writes each chunk back to its original offset. Output order therefore never
// depends on scheduling. Any worker exception is rethrown from here after the
// pool has been shut down.
ParallelResult render_parallel(const Image& input, const Lut3D& lut,
                               const RenderOptions& options, int thread_count,
                               std::size_t chunk_pixels = 4096);

// Per-channel maximum absolute difference between two same-sized images.
struct Deviation {
  double r = 0.0;
  double g = 0.0;
  double b = 0.0;

  double max() const { return std::max({r, g, b}); }
};

// Throws ExitCode::Usage when dimensions differ, so a comparison against the
// wrong fixture cannot masquerade as a rendering bug.
Deviation compare_images(const Image& a, const Image& b);

}  // namespace lutcheck
