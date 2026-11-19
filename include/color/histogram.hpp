#pragma once

#include <vector>

#include "color/types.hpp"

namespace color {

// Bucket for one channel: values are clamped to [0,1] and split into 16 equal
// buckets. Values outside [0,1] (possible in XYZ) land in the end buckets, so
// each group's counts always sum to the pixel count.
int histogramBucket(double value);

// Builds R/G/B histograms. Throws ColorError on non-finite pixel data.
ChannelHistograms buildHistograms(const std::vector<RGB>& pixels);
ChannelHistograms buildHistograms(const Image& image);

}  // namespace color
