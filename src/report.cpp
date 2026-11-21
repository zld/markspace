#include "lut/report.hpp"

#include <algorithm>
#include <cmath>

namespace lut {

double Deviation::worst() const {
    return std::max({std::fabs(maxAbs.r), std::fabs(maxAbs.g),
                     std::fabs(maxAbs.b)});
}

std::uint64_t ImageStats::beforeTotal(int channel) const {
    std::uint64_t total = 0;
    for (std::uint64_t n : before[channel]) {
        total += n;
    }
    return total;
}

std::uint64_t ImageStats::afterTotal(int channel) const {
    std::uint64_t total = 0;
    for (std::uint64_t n : after[channel]) {
        total += n;
    }
    return total;
}

Deviation measureDeviation(const std::vector<RGB>& reference,
                           const std::vector<RGB>& baked) {
    if (reference.size() != baked.size()) {
        throw LutDataError("deviation input size mismatch");
    }
    Deviation result;
    result.count = reference.size();
    for (std::size_t i = 0; i < reference.size(); ++i) {
        const double dr = std::fabs(reference[i].r - baked[i].r);
        const double dg = std::fabs(reference[i].g - baked[i].g);
        const double db = std::fabs(reference[i].b - baked[i].b);
        result.maxAbs.r = std::max(result.maxAbs.r, dr);
        result.maxAbs.g = std::max(result.maxAbs.g, dg);
        result.maxAbs.b = std::max(result.maxAbs.b, db);
    }
    return result;
}

void accumulateHistogram(Histogram& hist, double value) {
    if (!std::isfinite(value) || value < 0.0 || value > 1.0) {
        return;  // Counted separately as out-of-domain / clipped.
    }
    int bucket = static_cast<int>(value * kHistogramBuckets);
    if (bucket >= kHistogramBuckets) {
        bucket = kHistogramBuckets - 1;  // value == 1.0 lands in the top bin.
    }
    ++hist[bucket];
}

}  // namespace lut
