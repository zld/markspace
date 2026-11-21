#include "lut/render.hpp"

#include <algorithm>

namespace lut {
namespace {

bool inUnitRange(double v) { return v >= 0.0 && v <= 1.0; }

// One pixel through the shared kernel; updates histogram and counters.
RGB gradePixel(const Interpolator& interpolator, const RGB& input,
               ImageStats& stats) {
    const double channels[3] = {input.r, input.g, input.b};
    bool outside = false;
    for (int c = 0; c < 3; ++c) {
        if (inUnitRange(channels[c])) {
            accumulateHistogram(stats.before[c], channels[c]);
        } else {
            outside = true;
        }
    }
    if (outside) {
        ++stats.outOfDomain;
    }

    const RGB output = renderProbe(interpolator, input);
    const double outChannels[3] = {output.r, output.g, output.b};
    double graded[3];
    for (int c = 0; c < 3; ++c) {
        double value = outChannels[c];
        if (inUnitRange(value)) {
            accumulateHistogram(stats.after[c], value);
        } else {
            ++stats.clipped;
            value = std::clamp(value, 0.0, 1.0);
            accumulateHistogram(stats.after[c], value);
        }
        graded[c] = value;
    }
    return RGB{graded[0], graded[1], graded[2]};
}

}  // namespace

RGB renderProbe(const Interpolator& interpolator, const RGB& input) {
    return interpolator.sample(input);
}

RenderResult renderImage(const Lut3D& lut, const Image& input,
                         DomainPolicy policy, int tileRows) {
    if (tileRows <= 0) {
        throw UsageError("tile-rows must be a positive integer");
    }
    RenderResult result;
    result.graded.width = input.width;
    result.graded.height = input.height;
    result.graded.maxval = input.maxval;
    result.graded.pixels.resize(input.pixelCount());

    const Interpolator interpolator(lut, policy);
    const std::size_t rowWidth = input.width;

    // Tiled row bands: boundaries are independent because each pixel is
    // sampled on its own, so the tile size cannot change any output value.
    for (std::uint32_t rowStart = 0; rowStart < input.height;
         rowStart += static_cast<std::uint32_t>(tileRows)) {
        const std::uint32_t rowEnd =
            std::min(input.height,
                     rowStart + static_cast<std::uint32_t>(tileRows));
        for (std::uint32_t y = rowStart; y < rowEnd; ++y) {
            for (std::uint32_t x = 0; x < input.width; ++x) {
                const std::size_t idx =
                    static_cast<std::size_t>(y) * rowWidth + x;
                result.graded.pixels[idx] = gradePixel(
                    interpolator, input.pixels[idx], result.stats);
            }
        }
    }
    return result;
}

}  // namespace lut
