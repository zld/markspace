#include "lut/baker.hpp"

#include <cmath>

#include "lut/interpolator.hpp"

namespace lut {
namespace {

// The full colour pipeline for one encoded input coordinate.
RGB gradeOnce(const RGB& encodedInput, const ColorSpace& sourceSpace,
              const ColorSpace& targetSpace, const Interpolator& sourceLut) {
    // 1. Input decode: encoded signal -> linear source RGB.
    RGB linearIn = sourceSpace.decode(encodedInput);
    // 2. Space conversion: linear source RGB -> linear target RGB.
    RGB linearTarget = sourceSpace.convertLinear(linearIn, targetSpace);
    // 3. LUT sampling on the encoded table. The table stores encoded values,
    //    so coordinates and returned values stay in encoded target space.
    RGB encodedLutInput = targetSpace.encode(linearTarget);
    RGB encodedOutput = sourceLut.sample(encodedLutInput);
    if (!std::isfinite(encodedOutput.r) || !std::isfinite(encodedOutput.g) ||
        !std::isfinite(encodedOutput.b)) {
        throw LutDataError("non-finite value produced by bake pipeline");
    }
    return encodedOutput;
}

}  // namespace

std::vector<RGB> makeProbeGrid(int grid) {
    if (grid < 2 || grid > 65) {
        throw UsageError("probe grid size out of range [2,65]");
    }
    std::vector<RGB> points;
    points.reserve(static_cast<std::size_t>(grid) * grid * grid);
    for (int b = 0; b < grid; ++b) {
        for (int g = 0; g < grid; ++g) {
            for (int r = 0; r < grid; ++r) {
                points.push_back(
                    RGB{static_cast<double>(r) / (grid - 1),
                        static_cast<double>(g) / (grid - 1),
                        static_cast<double>(b) / (grid - 1)});
            }
        }
    }
    return points;
}

BakeResult bakeLut(const Lut3D& source, const ColorSpace& sourceSpace,
                   const ColorSpace& targetSpace, int targetSize,
                   int probeGrid, DomainPolicy policy) {
    if (targetSize < 2 || targetSize > 65) {
        throw UsageError("target LUT size out of range [2,65]");
    }
    const Interpolator sourceSampler(source, policy);

    BakeResult result;
    result.probeGrid = probeGrid;

    // Bake the target grid: red-fast node ordering matches the .cube layout.
    result.baked.size = targetSize;
    result.baked.nodes.resize(result.baked.nodeCount());
    for (int b = 0; b < targetSize; ++b) {
        for (int g = 0; g < targetSize; ++g) {
            for (int r = 0; r < targetSize; ++r) {
                const RGB encodedInput{
                    static_cast<double>(r) / (targetSize - 1),
                    static_cast<double>(g) / (targetSize - 1),
                    static_cast<double>(b) / (targetSize - 1)};
                result.baked.at(r, g, b) = gradeOnce(
                    encodedInput, sourceSpace, targetSpace, sourceSampler);
            }
        }
    }

    // Probe comparison: direct pipeline vs sampling the freshly baked table.
    const std::vector<RGB> probes = makeProbeGrid(probeGrid);
    result.probeCount = probes.size();
    result.referenceSamples.reserve(probes.size());
    result.bakedSamples.reserve(probes.size());
    const Interpolator bakedSampler(result.baked, DomainPolicy::Clamp);
    for (const RGB& probe : probes) {
        result.referenceSamples.push_back(gradeOnce(
            probe, sourceSpace, targetSpace, sourceSampler));
        result.bakedSamples.push_back(bakedSampler.sample(probe));
    }
    result.deviation =
        measureDeviation(result.referenceSamples, result.bakedSamples);
    return result;
}

}  // namespace lut
