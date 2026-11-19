#include "color/lut_kernel.hpp"

#include <algorithm>
#include <cmath>
#include <sstream>

namespace color {

namespace {

struct Cell {
    int index = 0;
    double fraction = 0.0;
};

Cell locate(double coordinate, int gridSize) {
    if (!std::isfinite(coordinate)) {
        throw ColorError("LUT lookup coordinate is not finite");
    }
    const double last = static_cast<double>(gridSize - 1);
    if (coordinate <= 0.0) return {0, 0.0};
    if (coordinate >= last) return {gridSize - 2, 1.0};
    const int base = static_cast<int>(std::floor(coordinate));
    return {base, coordinate - static_cast<double>(base)};
}

double lerp(double a, double b, double t) { return a + (b - a) * t; }

RGB lerp(const RGB& a, const RGB& b, double t) {
    return {lerp(a.r, b.r, t), lerp(a.g, b.g, t), lerp(a.b, b.b, t)};
}

std::string describeOutOfDomain(const RGB& value, const Domain& domain) {
    std::ostringstream out;
    out << "pixel outside LUT domain: (" << value.r << ", " << value.g << ", " << value.b
        << ") not in [" << domain.min.r << ", " << domain.min.g << ", " << domain.min.b
        << "]..[" << domain.max.r << ", " << domain.max.g << ", " << domain.max.b << "]";
    return out.str();
}

}  // namespace

RGB domainToGrid(const LUT& lut, const RGB& value) {
    if (!isFinite(value)) throw ColorError("LUT lookup received a non-finite value");
    const RGB width = lut.domain.width();
    const RGB span{static_cast<double>(lut.nx - 1), static_cast<double>(lut.ny - 1),
                   static_cast<double>(lut.nz - 1)};
    RGB grid{(value.r - lut.domain.min.r) / width.r * span.r,
             (value.g - lut.domain.min.g) / width.g * span.g,
             (value.b - lut.domain.min.b) / width.b * span.b};
    // Snap exactly-max inputs to the far grid node so rounding in the division
    // cannot select the previous cell. sampleLut would still be in bounds, but
    // the sampled value could differ by a fraction of a cell.
    if (value.r == lut.domain.max.r) grid.r = span.r;
    if (value.g == lut.domain.max.g) grid.g = span.g;
    if (value.b == lut.domain.max.b) grid.b = span.b;
    return grid;
}

RGB sampleLut(const LUT& lut, const RGB& grid) {
    const Cell cx = locate(grid.r, lut.nx);
    const Cell cy = locate(grid.g, lut.ny);
    const Cell cz = locate(grid.b, lut.nz);

    const double fr = cx.fraction;
    const double fg = cy.fraction;
    const double fb = cz.fraction;

    const RGB c000 = lut.at(cx.index, cy.index, cz.index);
    const RGB c100 = lut.at(cx.index + 1, cy.index, cz.index);
    const RGB c010 = lut.at(cx.index, cy.index + 1, cz.index);
    const RGB c110 = lut.at(cx.index + 1, cy.index + 1, cz.index);
    const RGB c001 = lut.at(cx.index, cy.index, cz.index + 1);
    const RGB c101 = lut.at(cx.index + 1, cy.index, cz.index + 1);
    const RGB c011 = lut.at(cx.index, cy.index + 1, cz.index + 1);
    const RGB c111 = lut.at(cx.index + 1, cy.index + 1, cz.index + 1);

    const RGB c00 = lerp(c000, c100, fr);
    const RGB c10 = lerp(c010, c110, fr);
    const RGB c01 = lerp(c001, c101, fr);
    const RGB c11 = lerp(c011, c111, fr);
    const RGB c0 = lerp(c00, c10, fg);
    const RGB c1 = lerp(c01, c11, fg);
    return lerp(c0, c1, fb);
}

RGB applyLut(const LUT& lut, const RGB& value, BoundaryPolicy policy,
             std::size_t pixelIndex) {
    if (!isFinite(value)) {
        std::string message = "non-finite pixel value";
        if (pixelIndex != kNoPixel) message += " at pixel " + std::to_string(pixelIndex);
        throw ProcessingError(message);
    }
    RGB bounded = value;
    if (!lut.domain.contains(value)) {
        if (policy == BoundaryPolicy::Reject) {
            std::string message = describeOutOfDomain(value, lut.domain);
            if (pixelIndex != kNoPixel) message += " at pixel " + std::to_string(pixelIndex);
            throw ProcessingError(message);
        }
        bounded = componentMax(componentMin(value, lut.domain.max), lut.domain.min);
    }
    return sampleLut(lut, domainToGrid(lut, bounded));
}

}  // namespace color
