#include "lut/baker.hpp"

#include <cmath>

#include "lut/interpolator.hpp"

namespace lut {
namespace {

// The full colour-managed pipeline for one target-encoded input coordinate.
// The source LUT grades source-encoded values, so coordinates are mapped into
// the source space before sampling and the graded result is mapped back. This
// is the explicit sequence required by the spec: input decode, space
// conversion, LUT sampling, output encode (naively copying node values across
// spaces would change the grade and is deliberately avoided).
RGB gradeOnce(const RGB& encodedInput, const ColorSpace& sourceSpace,
              const ColorSpace& targetSpace, const Interpolator& sourceLut) {
    // 1. Input decode: target-encoded signal -> linear target RGB.
    RGB linearTargetIn = targetSpace.decode(encodedInput);
    // 2a. Space conversion: linear target RGB -> linear source RGB, the
    //     coordinate system the source LUT actually lives in.
    RGB linearSourceIn = targetSpace.convertLinear(linearTargetIn, sourceSpace);
    // 2b. Encode to the source transfer to obtain a valid source-LUT address.
    RGB encodedLutInput = sourceSpace.encode(linearSourceIn);
    // 3. LUT sampling through the shared trilinear kernel. The table stores
    //    source-encoded graded values.
    RGB encodedLutOutput = sourceLut.sample(encodedLutInput);
    // 2c. Decode the graded source-encoded result back to linear source RGB.
    RGB linearSourceOut = sourceSpace.decode(encodedLutOutput);
    // 2d. Convert linear source RGB -> linear target RGB.
    RGB linearTargetOut = sourceSpace.convertLinear(linearSourceOut, targetSpace);
    // 4. Output encode: linear target RGB -> target-encoded stored value.
    RGB encodedOutput = targetSpace.encode(linearTargetOut);
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
