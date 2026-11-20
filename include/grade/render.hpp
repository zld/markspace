#ifndef GRADE_RENDER_HPP
#define GRADE_RENDER_HPP

#include <array>
#include <cstdint>

#include "grade/cli.hpp"
#include "grade/cube_lut.hpp"
#include "grade/image_ppm.hpp"

namespace grade {

constexpr int kHistogramBins = 16;
constexpr int kMaxWorkerThreads = 256;

using Histogram = std::array<uint64_t, kHistogramBins>;

struct ChannelHistograms {
    Histogram r{};
    Histogram g{};
    Histogram b{};
};

struct RenderResult {
    int lutGrid = 0;
    size_t pixels = 0;
    uint64_t outOfBounds = 0;
    int threads = 1;
    ChannelHistograms before; // encoded values entering the LUT stage
    ChannelHistograms after;  // encoded values written to the output
};

struct RenderConfig {
    ColorSpaceId inputSpace = ColorSpaceId::srgb;
    ColorSpaceId lutSpace = ColorSpaceId::srgb;
    ColorSpaceId outputSpace = ColorSpaceId::srgb;
    InterpId interpolation = InterpId::trilinear;
    OobId outOfBounds = OobId::clamp;
    int threads = 1;
};

// Shared interpolation kernel used by both serial and parallel paths so
// the two schedules cannot drift apart numerically. Coordinates are first
// normalized against the LUT domain; when `outOfBounds` is clamp, values
// outside [0,1] are clamped and the counter is incremented, otherwise a
// RenderError is thrown.
CubeLut::Rgb sampleLut(const CubeLut& lut, const CubeLut::Rgb& normalized,
                       InterpId interpolation, OobId outOfBounds,
                       uint64_t& clampCounter);

// Full per-pixel grade: decode input, move into LUT space, sample, move
// to output space, encode. Throws RenderError on non-finite values.
// `beforeEncoded` (when non-null) receives the pixel encoded in the LUT
// color space, used for the pre-grade histogram.
PpmImage::Pixel gradePixel(const PpmImage::Pixel& input, const CubeLut& lut,
                           const RenderConfig& config,
                           uint64_t& clampCounter,
                           PpmImage::Pixel* beforeEncoded = nullptr);

// Renders the image on the calling thread.
RenderResult renderSerial(const PpmImage& input, const CubeLut& lut,
                          const RenderConfig& config, PpmImage& output);

// Renders in contiguous row chunks on a worker pool. Worker exceptions are
// rethrown on the calling thread; the output image is only valid when the
// call returns normally.
RenderResult renderParallel(const PpmImage& input, const CubeLut& lut,
                            const RenderConfig& config, PpmImage& output,
                            int threads);

// Dispatches serial (threads == 1) or parallel rendering.
RenderResult renderImage(const PpmImage& input, const CubeLut& lut,
                         const RenderConfig& config, PpmImage& output);

// Per-channel maximum absolute difference between two equal-sized images.
std::array<double, 3> maximumChannelDifference(const PpmImage& a,
                                               const PpmImage& b);

// Adds one encoded [0,1] sample to the matching histogram bin.
void accumulateHistogram(ChannelHistograms& histograms,
                         const PpmImage::Pixel& pixel);

} // namespace grade

#endif
