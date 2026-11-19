#include "color/histogram.hpp"

#include <algorithm>
#include <cmath>
#include <string>

namespace color {

int histogramBucket(double value) {
    if (!std::isfinite(value)) {
        throw ColorError("histogram received a non-finite value");
    }
    const double clamped = std::min(1.0, std::max(0.0, value));
    int bucket = static_cast<int>(std::floor(clamped * static_cast<double>(kHistogramBuckets)));
    if (bucket >= static_cast<int>(kHistogramBuckets)) bucket = kHistogramBuckets - 1;
    if (bucket < 0) bucket = 0;
    return bucket;
}

ChannelHistograms buildHistograms(const std::vector<RGB>& pixels) {
    ChannelHistograms result;
    for (std::size_t i = 0; i < pixels.size(); ++i) {
        const RGB& pixel = pixels[i];
        if (!isFinite(pixel)) {
            throw ColorError("non-finite pixel " + std::to_string(i) + " in histogram");
        }
        for (int channel = 0; channel < 3; ++channel) {
            Histogram& histogram =
                channel == 0 ? result.r : (channel == 1 ? result.g : result.b);
            ++histogram.buckets[static_cast<std::size_t>(histogramBucket(pixel[channel]))];
        }
    }
    return result;
}

ChannelHistograms buildHistograms(const Image& image) { return buildHistograms(image.pixels); }

}  // namespace color
