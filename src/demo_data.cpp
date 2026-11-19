#include "color/demo.hpp"

#include <algorithm>
#include <cmath>
#include <sstream>

#include "color/cube_parser.hpp"

namespace color {

namespace {

// splitmix64: tiny, deterministic and portable across standard libraries.
class SplitMix64 {
public:
    explicit SplitMix64(std::uint64_t seed) : state_(seed) {}

    std::uint64_t next() {
        state_ += 0x9E3779B97F4A7C15ULL;
        std::uint64_t z = state_;
        z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9ULL;
        z = (z ^ (z >> 27)) * 0x94D049BB133111EBULL;
        return z ^ (z >> 31);
    }

    // Uniform in [0,1) using 53 significant bits.
    double uniform() {
        return static_cast<double>(next() >> 11) * (1.0 / 9007199254740992.0);
    }

private:
    std::uint64_t state_;
};

double smoothStep(double edge0, double edge1, double x) {
    const double t = std::min(1.0, std::max(0.0, (x - edge0) / (edge1 - edge0)));
    return t * t * (3.0 - 2.0 * t);
}

}  // namespace

LUT makeDemoLut(int size) {
    if (size < kMinLutSize || size > kMaxLutSize) {
        throw LutError("demo LUT size out of range: " + std::to_string(size));
    }
    LUT lut;
    lut.nx = lut.ny = lut.nz = size;
    lut.domain = Domain{{0.0, 0.0, 0.0}, {1.0, 1.0, 1.0}};
    lut.data.reserve(lut.sampleCount());
    const double last = static_cast<double>(size - 1);
    for (int z = 0; z < size; ++z) {
        for (int y = 0; y < size; ++y) {
            for (int x = 0; x < size; ++x) {
                const double r = static_cast<double>(x) / last;
                const double g = static_cast<double>(y) / last;
                const double b = static_cast<double>(z) / last;
                // Separable creative transform: channel-selective contrast plus
                // a gentle highlight roll-off. Endpoints stay exact so grid
                // nodes are predictable for tests.
                const double luma = 0.2126 * r + 0.7152 * g + 0.0722 * b;
                const double shoulder = smoothStep(0.7, 1.0, luma);
                const double gainR = 1.0 - 0.12 * shoulder;
                const double gainG = 1.0 - 0.04 * shoulder;
                const double liftB = 0.015 * (1.0 - b);
                const RGB sample{
                    std::min(1.0, r * gainR + 0.010 * (g - r) * (1.0 - r)),
                    std::min(1.0, g * gainG),
                    std::min(1.0, b + liftB * (1.0 - b)),
                };
                lut.data.push_back(sample);
            }
        }
    }
    return lut;
}

std::vector<RGB> makeDemoPixels(std::size_t count, ColorSpace inputSpace, std::uint64_t seed) {
    std::vector<RGB> pixels;
    pixels.reserve(count);
    SplitMix64 random(seed);
    // Fixed anchor schedule: gradient, boundary values, primaries, then noise.
    const std::size_t anchors = 16;
    const RGB anchorValues[anchors] = {
        {0.0, 0.0, 0.0}, {1.0, 1.0, 1.0}, {1.0, 0.0, 0.0}, {0.0, 1.0, 0.0},
        {0.0, 0.0, 0.5}, {0.5, 0.5, 0.5}, {1.0, 0.5, 0.0}, {1.0, 1.0, 0.0},
        {0.0, 1.0, 1.0}, {1.0, 0.0, 1.0}, {0.25, 0.5, 0.75}, {0.75, 0.5, 0.25},
        {1e-6, 0.5, 1.0 - 1e-6}, {0.04045, 0.0031308, 0.5}, {0.5, 0.0031308, 0.04045},
        {0.9, 0.9, 0.9},
    };
    for (std::size_t i = 0; i < count; ++i) {
        RGB value;
        if (i < anchors) {
            value = anchorValues[i];
        } else if (i < anchors * 2) {
            // Smooth gradient sweep across the cube.
            const double t = static_cast<double>(i - anchors) / static_cast<double>(anchors - 1);
            value = {t, 1.0 - t, 0.5 * (1.0 + std::sin(t * 3.14159265358979323846))};
        } else {
            value = {random.uniform(), random.uniform(), random.uniform()};
        }
        // Generated pixels live in the requested input space; sRGB values are
        // restricted to the encoded [0,1] range, linear/XYZ may exceed it.
        if (inputSpace == ColorSpace::Srgb) {
            for (int channel = 0; channel < 3; ++channel) {
                value[channel] = std::min(1.0, std::max(0.0, value[channel]));
            }
        }
        pixels.push_back(value);
    }
    return pixels;
}

std::string cubeTextWithOrder(const LUT& lut, const std::string& order) {
    validateLut(lut);
    if (order != "rgb" && order != "bgr") {
        throw std::invalid_argument("cube order must be rgb or bgr: " + order);
    }
    std::ostringstream out;
    out.precision(9);
    out << "TITLE \"embedded verification LUT\"\n";
    out << "LUT_3D_SIZE " << lut.nx << " " << lut.ny << " " << lut.nz << "\n";
    out << "DOMAIN_MIN " << lut.domain.min.r << " " << lut.domain.min.g << " "
        << lut.domain.min.b << "\n";
    out << "DOMAIN_MAX " << lut.domain.max.r << " " << lut.domain.max.g << " "
        << lut.domain.max.b << "\n";
    if (order == "bgr") out << "LUT_3D_ORDER BGR\n";
    for (int z = 0; z < lut.nz; ++z) {
        for (int y = 0; y < lut.ny; ++y) {
            for (int x = 0; x < lut.nx; ++x) {
                const RGB& sample = lut.at(x, y, z);
                if (order == "rgb") {
                    out << sample.r << " " << sample.g << " " << sample.b << "\n";
                } else {
                    out << sample.b << " " << sample.g << " " << sample.r << "\n";
                }
            }
        }
    }
    return out.str();
}

}  // namespace color
