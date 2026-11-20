#include "grade/render.hpp"

#include <cmath>
#include <thread>
#include <vector>

#include "grade/color.hpp"
#include "grade/error.hpp"

namespace grade {

namespace {

// Bins an encoded sample into the fixed 16-bucket histogram.
int histogramBin(double value) {
    if (value <= 0.0) {
        return 0;
    }
    if (value >= 1.0) {
        return kHistogramBins - 1;
    }
    auto bin = static_cast<int>(value * kHistogramBins);
    return bin;
}

// Per-channel OOB policy applied after domain normalization.
CubeLut::Rgb applyBounds(const CubeLut::Rgb& normalized, OobId policy,
                         uint64_t& clampCounter) {
    CubeLut::Rgb result = normalized;
    for (size_t channel = 0; channel < 3; ++channel) {
        if (!std::isfinite(result[channel])) {
            throw RenderError("non-finite coordinate reached the LUT");
        }
        if (result[channel] < 0.0 || result[channel] > 1.0) {
            if (policy == OobId::error) {
                throw RenderError(
                    "pixel falls outside the LUT domain and "
                    "--out-of-bounds error is set");
            }
            result[channel] =
                result[channel] < 0.0 ? 0.0 : 1.0;
            ++clampCounter;
        }
    }
    return result;
}

const CubeLut::Rgb& corner(const CubeLut& lut, int r, int g, int b) {
    int n = lut.gridSize();
    r = std::min(std::max(r, 0), n - 1);
    g = std::min(std::max(g, 0), n - 1);
    b = std::min(std::max(b, 0), n - 1);
    return lut.data()[lut.index(r, g, b)];
}

CubeLut::Rgb trilinear(const CubeLut& lut, const CubeLut::Rgb& q) {
    int n = lut.gridSize();
    double scaledR = q[0] * static_cast<double>(n - 1);
    double scaledG = q[1] * static_cast<double>(n - 1);
    double scaledB = q[2] * static_cast<double>(n - 1);
    int r0 = static_cast<int>(std::floor(scaledR));
    int g0 = static_cast<int>(std::floor(scaledG));
    int b0 = static_cast<int>(std::floor(scaledB));
    r0 = std::min(r0, n - 2);
    g0 = std::min(g0, n - 2);
    b0 = std::min(b0, n - 2);
    double tr = scaledR - static_cast<double>(r0);
    double tg = scaledG - static_cast<double>(g0);
    double tb = scaledB - static_cast<double>(b0);

    CubeLut::Rgb result{{0.0, 0.0, 0.0}};
    for (int b = 0; b < 2; ++b) {
        for (int g = 0; g < 2; ++g) {
            for (int r = 0; r < 2; ++r) {
                const CubeLut::Rgb& c = corner(lut, r0 + r, g0 + g, b0 + b);
                double weight = (r ? tr : 1.0 - tr) *
                                (g ? tg : 1.0 - tg) *
                                (b ? tb : 1.0 - tb);
                for (size_t channel = 0; channel < 3; ++channel) {
                    result[channel] += c[channel] * weight;
                }
            }
        }
    }
    return result;
}

// Tetrahedral interpolation: locate the cell, then pick the tetrahedron
// containing the fractional point and barycentrically blend its 4 nodes.
CubeLut::Rgb tetrahedral(const CubeLut& lut, const CubeLut::Rgb& q) {
    int n = lut.gridSize();
    double scaledR = q[0] * static_cast<double>(n - 1);
    double scaledG = q[1] * static_cast<double>(n - 1);
    double scaledB = q[2] * static_cast<double>(n - 1);
    int r0 = std::min(static_cast<int>(std::floor(scaledR)), n - 2);
    int g0 = std::min(static_cast<int>(std::floor(scaledG)), n - 2);
    int b0 = std::min(static_cast<int>(std::floor(scaledB)), n - 2);
    double tr = scaledR - static_cast<double>(r0);
    double tg = scaledG - static_cast<double>(g0);
    double tb = scaledB - static_cast<double>(b0);

    const CubeLut::Rgb& c000 = corner(lut, r0, g0, b0);
    CubeLut::Rgb result{{0.0, 0.0, 0.0}};
    const CubeLut::Rgb *a, *b, *c;
    double wa, wb, wc;
    if (tr >= tg && tg >= tb) {
        // T1: c000, c100, c110, c111
        a = &corner(lut, r0 + 1, g0, b0);
        b = &corner(lut, r0 + 1, g0 + 1, b0);
        c = &corner(lut, r0 + 1, g0 + 1, b0 + 1);
        wa = tr - tg;
        wb = tg - tb;
        wc = tb;
    } else if (tg >= tr && tr >= tb) {
        // T2: c000, c100, c110, c011
        a = &corner(lut, r0 + 1, g0, b0);
        b = &corner(lut, r0 + 1, g0 + 1, b0);
        c = &corner(lut, r0, g0 + 1, b0 + 1);
        wa = tr - tb;
        wb = tg - tr;
        wc = tb;
    } else if (tg >= tb && tb >= tr) {
        // T3: c000, c100, c010, c011
        a = &corner(lut, r0 + 1, g0, b0);
        b = &corner(lut, r0, g0 + 1, b0);
        c = &corner(lut, r0, g0 + 1, b0 + 1);
        wa = tr;
        wb = tg - tb;
        wc = tb - tr;
    } else if (tb >= tg && tg >= tr) {
        // T4: c000, c001, c011, c111
        a = &corner(lut, r0, g0, b0 + 1);
        b = &corner(lut, r0, g0 + 1, b0 + 1);
        c = &corner(lut, r0 + 1, g0 + 1, b0 + 1);
        wa = tb - tg;
        wb = tg - tr;
        wc = tr;
    } else {
        // T6 (tr >= tb >= tg): c000, c001, c101, c111
        a = &corner(lut, r0, g0, b0 + 1);
        b = &corner(lut, r0 + 1, g0, b0 + 1);
        c = &corner(lut, r0 + 1, g0 + 1, b0 + 1);
        wa = tb - tr;
        wb = tr - tg;
        wc = tg;
    }
    double w0 = 1.0 - wa - wb - wc;
    for (size_t channel = 0; channel < 3; ++channel) {
        result[channel] = c000[channel] * w0 + (*a)[channel] * wa +
                          (*b)[channel] * wb + (*c)[channel] * wc;
    }
    return result;
}

} // namespace

CubeLut::Rgb sampleLut(const CubeLut& lut, const CubeLut::Rgb& normalizedIn,
                       InterpId interpolation, OobId outOfBounds,
                       uint64_t& clampCounter) {
    CubeLut::Rgb q = applyBounds(normalizedIn, outOfBounds, clampCounter);
    if (interpolation == InterpId::trilinear) {
        return trilinear(lut, q);
    }
    return tetrahedral(lut, q);
}

PpmImage::Pixel gradePixel(const PpmImage::Pixel& input, const CubeLut& lut,
                           const RenderConfig& config,
                           uint64_t& clampCounter,
                           PpmImage::Pixel* beforeEncoded) {
    if (!isFinite(input)) {
        throw RenderError("input pixel contains a non-finite sample");
    }
    Triple linear = decodeTransfer(config.inputSpace, input);
    Triple lutLinear =
        convertLinearRgb(config.inputSpace, config.lutSpace, linear);
    CubeLut::Rgb lutEncoded = encodeTransfer(config.lutSpace, lutLinear);
    if (!isFinite(lutEncoded)) {
        throw RenderError("non-finite value produced before LUT sampling");
    }
    if (beforeEncoded != nullptr) {
        *beforeEncoded = lutEncoded;
    }
    CubeLut::Rgb normalized = lut.normalizeToDomain(lutEncoded);
    CubeLut::Rgb graded = sampleLut(lut, normalized, config.interpolation,
                                    config.outOfBounds, clampCounter);
    if (!isFinite(graded)) {
        throw RenderError("LUT produced a non-finite value");
    }
    Triple gradedLinear = decodeTransfer(config.lutSpace, graded);
    Triple outLinear =
        convertLinearRgb(config.lutSpace, config.outputSpace, gradedLinear);
    PpmImage::Pixel output = encodeTransfer(config.outputSpace, outLinear);
    if (!isFinite(output)) {
        throw RenderError("non-finite value produced during output encoding");
    }
    return output;
}

void accumulateHistogram(ChannelHistograms& histograms,
                         const PpmImage::Pixel& pixel) {
    ++histograms.r[static_cast<size_t>(histogramBin(pixel[0]))];
    ++histograms.g[static_cast<size_t>(histogramBin(pixel[1]))];
    ++histograms.b[static_cast<size_t>(histogramBin(pixel[2]))];
}

RenderResult renderSerial(const PpmImage& input, const CubeLut& lut,
                          const RenderConfig& config, PpmImage& output) {
    RenderResult result;
    result.lutGrid = lut.gridSize();
    result.pixels = input.pixelCount();
    result.threads = 1;
    uint64_t clamps = 0;
    for (size_t i = 0; i < input.pixelCount(); ++i) {
        PpmImage::Pixel before;
        PpmImage::Pixel graded =
            gradePixel(input.at(i), lut, config, clamps, &before);
        accumulateHistogram(result.before, before);
        output.at(i) = graded;
        accumulateHistogram(result.after, graded);
    }
    result.outOfBounds = clamps;
    return result;
}

RenderResult renderParallel(const PpmImage& input, const CubeLut& lut,
                            const RenderConfig& config, PpmImage& output,
                            int threads) {
    if (threads <= 0 || threads > kMaxWorkerThreads) {
        throw UsageError("thread count must be in [1, " +
                         std::to_string(kMaxWorkerThreads) + "]");
    }
    if (input.width() != output.width() ||
        input.height() != output.height()) {
        throw RenderError("parallel render: output image size mismatch");
    }
    RenderResult result;
    result.lutGrid = lut.gridSize();
    result.pixels = input.pixelCount();
    result.threads = threads;

    int rows = input.height();
    int workers = std::min(threads, rows);
    std::vector<uint64_t> clampCounters(static_cast<size_t>(workers), 0);
    std::vector<ChannelHistograms> before(static_cast<size_t>(workers));
    std::vector<ChannelHistograms> after(static_cast<size_t>(workers));
    std::vector<std::exception_ptr> failures(static_cast<size_t>(workers));

    auto worker = [&](int workerId, int rowBegin, int rowEnd) {
        try {
            uint64_t& localClamps =
                clampCounters[static_cast<size_t>(workerId)];
            ChannelHistograms& localBefore =
                before[static_cast<size_t>(workerId)];
            ChannelHistograms& localAfter =
                after[static_cast<size_t>(workerId)];
            for (int row = rowBegin; row < rowEnd; ++row) {
                size_t first = static_cast<size_t>(row) *
                               static_cast<size_t>(input.width());
                size_t last = first + static_cast<size_t>(input.width());
                for (size_t i = first; i < last; ++i) {
                    PpmImage::Pixel beforeEncoded;
                    PpmImage::Pixel graded = gradePixel(
                        input.at(i), lut, config, localClamps,
                        &beforeEncoded);
                    output.at(i) = graded;
                    accumulateHistogram(localBefore, beforeEncoded);
                    accumulateHistogram(localAfter, graded);
                }
            }
        } catch (...) {
            // Hand back to the caller; the output image is not touched
            // beyond this worker's chunk and must not be published.
            failures[static_cast<size_t>(workerId)] = std::current_exception();
        }
    };

    std::vector<std::thread> pool;
    pool.reserve(static_cast<size_t>(workers));
    int rowsPerWorker = rows / workers;
    int remainder = rows % workers;
    int rowCursor = 0;
    for (int workerId = 0; workerId < workers; ++workerId) {
        int chunk = rowsPerWorker + (workerId < remainder ? 1 : 0);
        int rowBegin = rowCursor;
        int rowEnd = rowCursor + chunk;
        rowCursor = rowEnd;
        pool.emplace_back(worker, workerId, rowBegin, rowEnd);
    }

    // Join every worker before touching any exception so a throwing task
    // cannot leave a joinable thread behind, then surface the first error.
    for (auto& thread : pool) {
        thread.join();
    }
    for (const auto& failure : failures) {
        if (failure) {
            std::rethrow_exception(failure);
        }
    }

    for (int workerId = 0; workerId < workers; ++workerId) {
        result.outOfBounds += clampCounters[static_cast<size_t>(workerId)];
        const ChannelHistograms& localBefore =
            before[static_cast<size_t>(workerId)];
        const ChannelHistograms& localAfter =
            after[static_cast<size_t>(workerId)];
        for (size_t bin = 0; bin < kHistogramBins; ++bin) {
            result.before.r[bin] += localBefore.r[bin];
            result.before.g[bin] += localBefore.g[bin];
            result.before.b[bin] += localBefore.b[bin];
            result.after.r[bin] += localAfter.r[bin];
            result.after.g[bin] += localAfter.g[bin];
            result.after.b[bin] += localAfter.b[bin];
        }
    }
    return result;
}

RenderResult renderImage(const PpmImage& input, const CubeLut& lut,
                         const RenderConfig& config, PpmImage& output) {
    if (config.threads <= 1) {
        return renderSerial(input, lut, config, output);
    }
    return renderParallel(input, lut, config, output, config.threads);
}

std::array<double, 3> maximumChannelDifference(const PpmImage& a,
                                               const PpmImage& b) {
    if (a.width() != b.width() || a.height() != b.height()) {
        throw RenderError("comparison requires images of equal size");
    }
    std::array<double, 3> maxDiff{{0.0, 0.0, 0.0}};
    for (size_t i = 0; i < a.pixelCount(); ++i) {
        for (size_t channel = 0; channel < 3; ++channel) {
            maxDiff[channel] =
                std::max(maxDiff[channel],
                         std::fabs(a.at(i)[channel] - b.at(i)[channel]));
        }
    }
    return maxDiff;
}

} // namespace grade
