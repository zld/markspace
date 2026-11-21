#ifndef LUT_RENDER_HPP
#define LUT_RENDER_HPP

#include "lut/image.hpp"
#include "lut/interpolator.hpp"
#include "lut/model.hpp"
#include "lut/report.hpp"

namespace lut {

// Grades a single encoded RGB sample through the shared trilinear kernel.
RGB renderProbe(const Interpolator& lut, const RGB& input);

struct RenderResult {
    Image graded;
    ImageStats stats;
};

// Grades an image through the LUT, processing rows in tiles of `tileRows`
// (the tile size affects scheduling only, never the result). Before/after
// 16-bucket histograms, out-of-domain and output clip counters are collected.
RenderResult renderImage(const Lut3D& lut, const Image& input,
                         DomainPolicy policy, int tileRows);

}  // namespace lut

#endif  // LUT_RENDER_HPP
