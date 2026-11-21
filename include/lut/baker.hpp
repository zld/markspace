#ifndef LUT_BAKER_HPP
#define LUT_BAKER_HPP

#include <vector>

#include "lut/color_space.hpp"
#include "lut/model.hpp"
#include "lut/report.hpp"

namespace lut {

struct BakeResult {
    Lut3D baked;                       // Encoded values in the target space.
    std::vector<RGB> referenceSamples; // Direct-pipeline results, encoded.
    std::vector<RGB> bakedSamples;     // Baked-table results, encoded.
    Deviation deviation;
    int probeGrid = 0;
    std::size_t probeCount = 0;
};

// Builds a uniform NxNxN probe grid over [0,1] (red-fast), N points per axis.
std::vector<RGB> makeProbeGrid(int grid);

// Rebakes `source` from sourceSpace to targetSpace at `targetSize` nodes.
// Every sample follows the explicit pipeline: input decode, linear RGB space
// conversion, source-LUT trilinear sampling, output encode. Probe samples
// compare the direct pipeline against the baked table for deviation reporting.
BakeResult bakeLut(const Lut3D& source, const ColorSpace& sourceSpace,
                   const ColorSpace& targetSpace, int targetSize,
                   int probeGrid, DomainPolicy policy);

}  // namespace lut

#endif  // LUT_BAKER_HPP
