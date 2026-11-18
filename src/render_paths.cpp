#include "render_paths.hpp"

#include <algorithm>
#include <atomic>
#include <cmath>
#include <exception>
#include <mutex>
#include <thread>
#include <vector>

#include "errors.hpp"
#include "interpolation.hpp"

namespace lutcheck {
namespace {

void validate_thread_count(int thread_count) {
  if (thread_count < 1 || thread_count > kMaxThreads) {
    fail(ExitCode::Usage, "thread count must be in [1, " +
                              std::to_string(kMaxThreads) + "]");
  }
}

}  // namespace

std::size_t run_chunked(std::size_t total, int thread_count,
                         std::size_t chunk_pixels,
                         const ChunkWork& body) {
  validate_thread_count(thread_count);
  if (chunk_pixels == 0) {
    fail(ExitCode::Usage, "chunk size must be positive");
  }
  if (total == 0) return 0;

  // Boundaries depend only on the item count and chunk size, never on how
  // many workers happen to pick up work.
  const std::size_t chunk_count = (total + chunk_pixels - 1) / chunk_pixels;
  const int workers =
      std::min<int>(thread_count, static_cast<int>(chunk_count));
  std::atomic<std::size_t> next_chunk{0};
  std::exception_ptr failure;
  std::mutex failure_mutex;

  auto worker = [&]() {
    try {
      for (;;) {
        const std::size_t index = next_chunk.fetch_add(1);
        if (index >= chunk_count) return;
        const std::size_t begin = index * chunk_pixels;
        const std::size_t end = std::min(begin + chunk_pixels, total);
        body(begin, end);
      }
    } catch (...) {
      // Stop handing out work and remember the first failure. The main thread
      // rethrows once every worker has joined, so no thread is left running.
      next_chunk.store(chunk_count);
      std::lock_guard<std::mutex> lock(failure_mutex);
      if (!failure) failure = std::current_exception();
    }
  };

  std::vector<std::thread> pool;
  pool.reserve(static_cast<std::size_t>(workers));
  for (int i = 0; i < workers; ++i) {
    pool.emplace_back(worker);
  }
  for (auto& thread : pool) {
    thread.join();
  }
  if (failure) {
    std::rethrow_exception(failure);
  }
  return workers;
}

Vec3 transform_sample(const Lut3D& lut, const RenderOptions& options,
                      const Vec3& input) {
  // The matrix maps the sample into the LUT's input space; interpolation then
  // applies the domain and clamps. Both paths call this exact function.
  const Vec3 mapped = options.input_to_lut.apply(input);
  (void)options.min_value;
  (void)options.max_value;
  if (!std::isfinite(mapped.r) || !std::isfinite(mapped.g) ||
      !std::isfinite(mapped.b)) {
    // A finite input through a finite matrix cannot overflow to infinity for
    // sane matrix magnitudes, but guard so a corrupt matrix cannot poison the
    // output with NaN.
    return {};
  }
  return sample_trilinear(lut, mapped);
}

Image render_scalar(const Image& input, const Lut3D& lut,
                    const RenderOptions& options) {
  Image output;
  output.width = input.width;
  output.height = input.height;
  output.pixels.resize(input.pixels.size());
  for (std::size_t i = 0; i < input.pixels.size(); ++i) {
    output.pixels[i] = transform_sample(lut, options, input.pixels[i]);
  }
  return output;
}

ParallelResult render_parallel(const Image& input, const Lut3D& lut,
                               const RenderOptions& options, int thread_count,
                               std::size_t chunk_pixels) {
  ParallelResult result;
  result.image.width = input.width;
  result.image.height = input.height;
  result.image.pixels.resize(input.pixels.size());
  result.threads_used = run_chunked(
      input.pixels.size(), thread_count, chunk_pixels,
      [&](std::size_t begin, std::size_t end) {
        for (std::size_t i = begin; i < end; ++i) {
          result.image.pixels[i] =
              transform_sample(lut, options, input.pixels[i]);
        }
      });
  return result;
}

Deviation compare_images(const Image& a, const Image& b) {
  if (a.width != b.width || a.height != b.height ||
      a.pixels.size() != b.pixels.size()) {
    fail(ExitCode::Usage, "cannot compare images of different sizes");
  }
  Deviation deviation;
  for (std::size_t i = 0; i < a.pixels.size(); ++i) {
    for (std::size_t channel = 0; channel < 3; ++channel) {
      const double diff = std::fabs(a.pixels[i][channel] - b.pixels[i][channel]);
      double& current = channel == 0 ? deviation.r : channel == 1
                                                            ? deviation.g
                                                            : deviation.b;
      current = std::max(current, diff);
    }
  }
  return deviation;
}

}  // namespace lutcheck
