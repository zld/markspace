#include "lut/interpolator.hpp"

#include <algorithm>
#include <cmath>

namespace lut {
namespace {

double applyDomain(double value, double lo, double hi, int component,
                   DomainPolicy policy) {
    if (value < lo || value > hi) {
        if (policy == DomainPolicy::Error) {
            const char* axis = component == 0   ? "R"
                               : component == 1 ? "G"
                                                : "B";
            throw LutDataError(std::string("sample outside LUT domain on ") +
                               axis + ": " + std::to_string(value));
        }
        value = std::clamp(value, lo, hi);
    }
    return value;
}

double lerp(double a, double b, double t) { return a + (b - a) * t; }

}  // namespace

RGB Interpolator::sample(const RGB& position) const {
    const double in[3] = {position.r, position.g, position.b};
    double norm[3];
    int cell[3];
    double frac[3];

    for (int axis = 0; axis < 3; ++axis) {
        const double lo = lut_.domainMin[axis];
        const double hi = lut_.domainMax[axis];
        const double v = applyDomain(in[axis], lo, hi, axis, policy_);
        norm[axis] = (v - lo) / (hi - lo);
        double scaled = norm[axis] * (lut_.size - 1);
        double base = std::floor(scaled);
        int i = static_cast<int>(base);
        if (i >= lut_.size - 1) {
            i = lut_.size - 2;
            frac[axis] = 1.0;
        } else {
            frac[axis] = scaled - base;
        }
        cell[axis] = i;
    }

    // Shared trilinear kernel over the eight surrounding grid corners.
    RGB result{0.0, 0.0, 0.0};
    for (int b = 0; b < 2; ++b) {
        for (int g = 0; g < 2; ++g) {
            for (int r = 0; r < 2; ++r) {
                const RGB& corner =
                    lut_.at(cell[0] + r, cell[1] + g, cell[2] + b);
                const double weight =
                    (r ? frac[0] : 1.0 - frac[0]) *
                    (g ? frac[1] : 1.0 - frac[1]) *
                    (b ? frac[2] : 1.0 - frac[2]);
                result.r += corner.r * weight;
                result.g += corner.g * weight;
                result.b += corner.b * weight;
            }
        }
    }
    if (!std::isfinite(result.r) || !std::isfinite(result.g) ||
        !std::isfinite(result.b)) {
        throw LutDataError("non-finite interpolation result");
    }
    return result;
}

}  // namespace lut
