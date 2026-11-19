#include <cmath>
#include <cstdint>
#include <iostream>
#include <sstream>
#include <string>
#include <vector>

#include "color/cube_parser.hpp"
#include "color/demo.hpp"
#include "color/demo.hpp"
#include "color/histogram.hpp"
#include "color/lut_kernel.hpp"
#include "color/render_paths.hpp"
#include "color/report.hpp"

namespace {

using namespace color;

int g_failures = 0;

#define CHECK(condition)                                                        \
    do {                                                                        \
        if (!(condition)) {                                                     \
            std::cerr << __FILE__ << ":" << __LINE__ << ": CHECK failed: " #condition "\n"; \
            ++g_failures;                                                       \
        }                                                                       \
    } while (false)

#define CHECK_NEAR(a, b, tolerance)                                             \
    do {                                                                        \
        const double lhs_ = (a);                                                \
        const double rhs_ = (b);                                                \
        if (!(std::fabs(lhs_ - rhs_) <= (tolerance))) {                         \
            std::cerr << __FILE__ << ":" << __LINE__ << ": CHECK_NEAR failed: " \
                      << #a << "=" << lhs_ << " vs " << #b << "=" << rhs_ << " tol " \
                      << (tolerance) << "\n";                                   \
            ++g_failures;                                                       \
        }                                                                       \
    } while (false)

std::string cubeText(int size, int sampleLines = -1) {
    const int required = size * size * size;
    const int lines = sampleLines < 0 ? required : sampleLines;
    std::ostringstream out;
    out << "LUT_3D_SIZE " << size << "\n";
    for (int i = 0; i < lines; ++i) out << "0.1 0.2 0.3\n";
    return out.str();
}

std::string errorText(const std::string& text) {
    try {
        parseCubeText(text, "fixture.cube");
    } catch (const CubeError& error) {
        return error.what();
    }
    return {};
}

void testParserAcceptance() {
    const std::string text =
        "\xEF\xBB\xBF# comment\r\n"
        "TITLE \"studio look\"\r\n"
        "\r\n"
        "LUT_3D_SIZE 2 3 2\n"
        "DOMAIN_MIN -0.5 -0.25 0\n"
        "DOMAIN_MAX 1.5 1.25 1\n"
        "LUT_3D_ORDER BGR\n";
    std::ostringstream samples;
    for (int z = 0; z < 2; ++z) {
        for (int y = 0; y < 3; ++y) {
            for (int x = 0; x < 2; ++x) {
                // BGR on disk: b g r. Parser must store r g b.
                samples << (0.1 * z) << " " << (0.1 * y) << " " << (0.1 * x) << "\n";
            }
        }
    }
    const LUT lut = parseCubeText(text + samples.str(), "fixture.cube");
    CHECK(lut.nx == 2 && lut.ny == 3 && lut.nz == 2);
    CHECK(lut.sampleCount() == 12);
    CHECK(lut.data.size() == 12);
    CHECK_NEAR(lut.domain.min.r, -0.5, 0.0);
    CHECK_NEAR(lut.domain.max.g, 1.25, 0.0);
    // Disk line for x=1,y=2,z=1 is "0.1 0.2 0.1" (b g r), stored as r g b.
    CHECK_NEAR(lut.at(1, 2, 1).r, 0.1, 1e-15);
    CHECK_NEAR(lut.at(1, 2, 1).g, 0.2, 1e-15);
    CHECK_NEAR(lut.at(1, 2, 1).b, 0.1, 1e-15);
    // Red-fastest ordering.
    CHECK_NEAR(lut.data[0].r, 0.0, 1e-15);
    CHECK_NEAR(lut.data[1].r, 0.1, 1e-15);
    CHECK_NEAR(lut.data[2].g, 0.1, 1e-15);

    // Trailing comments on statements and samples are ignored.
    const std::string commented =
        "LUT_3D_SIZE 2   # cubic\n"
        "DOMAIN_MIN 0 0 0 # low\n"
        "DOMAIN_MAX 1 1 1\n"
        "0 0 0 # first sample\n"
        "0.5 0.5 0.5\n"
        "1 1 1\n"
        "0.25 0.25 0.25\n"
        "1 1 1\n"
        "1 1 1\n"
        "1 1 1\n"
        "1 1 1\n";
    CHECK(parseCubeText(commented, "commented.cube").sampleCount() == 8);

    // Round-trip through the serializer.
    const std::string serialized = serializeCube(lut, "round trip");
    const LUT again = parseCubeText(serialized, "serialized.cube");
    CHECK(again.data.size() == lut.data.size());
    for (std::size_t i = 0; i < lut.data.size(); ++i) {
        CHECK_NEAR(again.data[i].r, lut.data[i].r, 1e-8);
        CHECK_NEAR(again.data[i].g, lut.data[i].g, 1e-8);
        CHECK_NEAR(again.data[i].b, lut.data[i].b, 1e-8);
    }
}

void testParserRejections() {
    CHECK(!errorText(cubeText(1)).empty());
    CHECK(!errorText(cubeText(66, 66 * 66 * 66)).empty());
    CHECK(!errorText("LUT_3D_SIZE 2\nLUT_3D_SIZE 2\n").empty());
    CHECK(!errorText(cubeText(2, 7)).empty());
    CHECK(!errorText(cubeText(2, 9)).empty());
    CHECK(!errorText("LUT_3D_SIZE nope\n").empty());
    CHECK(!errorText("LUT_3D_SIZE 2\nDOMAIN_MIN 0 0 0\nDOMAIN_MAX 1 1 1\n0 0 inf\n"
                     "0 0 0\n0 0 0\n0 0 0\n0 0 0\n0 0 0\n0 0 0\n0 0 0\n").empty());
    CHECK(!errorText("LUT_3D_SIZE 2\n0 0 0 0\n").empty());
    CHECK(!errorText("LUT_3D_SIZE 2\nBOGUS 1 2 3\n").empty());
    CHECK(!errorText("0.1 0.2 0.3\n").empty());
    CHECK(!errorText("LUT_3D_SIZE 2\nDOMAIN_MIN 1 1 1\nDOMAIN_MAX 1 1 1\n").empty());
    CHECK(!errorText("LUT_3D_SIZE 2\nLUT_3D_ORDER XYZ\n").empty());

    // Line numbers must point at the offending statement, not the file end.
    const std::string lineNumberText =
        "TITLE \"x\"\n"
        "LUT_3D_SIZE 2\n"
        "DOMAIN_MIN 0 0 0\n"
        "DOMAIN_MAX 1 1 1\n"
        "0 0 0\n"
        "0 0 nan\n";
    const std::string message = errorText(lineNumberText);
    CHECK(message.find(":6:") != std::string::npos);
    CHECK(message.find("fixture.cube") != std::string::npos);

    const std::string shortCount =
        "LUT_3D_SIZE 2\n"
        "0 0 0\n"
        "0 0 0\n";
    const std::string countMessage = errorText(shortCount);
    CHECK(countMessage.find("sample count mismatch") != std::string::npos);
    // The shortfall is only known at end of input: report the EOF line.
    CHECK(countMessage.find(":4:") != std::string::npos);

    // Non-cubic sizes are supported; a zero-width channel is not.
    const LUT nonCubic = parseCubeText(cubeText(2), "ok.cube");
    CHECK(nonCubic.sampleCount() == 8);
}

LUT identityLut(int n, Domain domain = Domain{}) {
    LUT lut;
    lut.nx = lut.ny = lut.nz = n;
    lut.domain = domain;
    lut.data.reserve(lut.sampleCount());
    const RGB width = domain.width();
    for (int z = 0; z < n; ++z) {
        for (int y = 0; y < n; ++y) {
            for (int x = 0; x < n; ++x) {
                const double last = static_cast<double>(n - 1);
                lut.data.push_back({domain.min.r + width.r * static_cast<double>(x) / last,
                                    domain.min.g + width.g * static_cast<double>(y) / last,
                                    domain.min.b + width.b * static_cast<double>(z) / last});
            }
        }
    }
    return lut;
}

void testInterpolation() {
    const LUT lut = identityLut(3);
    // Grid nodes are exact.
    CHECK_NEAR(applyLut(lut, {0.0, 0.0, 0.0}, BoundaryPolicy::Clamp).r, 0.0, 1e-15);
    CHECK_NEAR(applyLut(lut, {0.5, 0.5, 0.5}, BoundaryPolicy::Clamp).g, 0.5, 1e-15);
    // Exactly at the maximum domain value.
    const RGB top = applyLut(lut, {1.0, 1.0, 1.0}, BoundaryPolicy::Clamp);
    CHECK_NEAR(top.r, 1.0, 1e-15);
    CHECK_NEAR(top.g, 1.0, 1e-15);
    CHECK_NEAR(top.b, 1.0, 1e-15);
    // Mid-cell trilinear blend of an identity LUT is the identity.
    const RGB mid = applyLut(lut, {0.17, 0.42, 0.83}, BoundaryPolicy::Clamp);
    CHECK_NEAR(mid.r, 0.17, 1e-14);
    CHECK_NEAR(mid.g, 0.42, 1e-14);
    CHECK_NEAR(mid.b, 0.83, 1e-14);

    // Out-of-domain: clamp folds to the edge, reject throws.
    const RGB clamped = applyLut(lut, {-2.0, 0.5, 4.0}, BoundaryPolicy::Clamp);
    CHECK_NEAR(clamped.r, 0.0, 1e-15);
    CHECK_NEAR(clamped.b, 1.0, 1e-15);
    CHECK_NEAR(clamped.g, 0.5, 1e-15);
    bool rejected = false;
    try {
        applyLut(lut, {-2.0, 0.5, 4.0}, BoundaryPolicy::Reject, 7);
    } catch (const ProcessingError& error) {
        rejected = std::string(error.what()).find("pixel 7") != std::string::npos;
    }
    CHECK(rejected);

    // Non-cubic grids interpolate independently per axis.
    LUT nonCubic;
    nonCubic.nx = 2;
    nonCubic.ny = 3;
    nonCubic.nz = 4;
    nonCubic.domain = Domain{{0, 0, 0}, {1, 1, 1}};
    for (int z = 0; z < 4; ++z) {
        for (int y = 0; y < 3; ++y) {
            for (int x = 0; x < 2; ++x) {
                nonCubic.data.push_back({static_cast<double>(x), static_cast<double>(y) / 2.0,
                                         static_cast<double>(z) / 3.0});
            }
        }
    }
    const RGB value = applyLut(nonCubic, {0.25, 0.75, 0.5}, BoundaryPolicy::Clamp);
    CHECK_NEAR(value.r, 0.25, 1e-14);
    CHECK_NEAR(value.g, 0.75, 1e-14);
    CHECK_NEAR(value.b, 0.5, 1e-14);
    const RGB edge = applyLut(nonCubic, {1.0, 1.0, 1.0}, BoundaryPolicy::Clamp);
    CHECK_NEAR(edge.r, 1.0, 1e-15);
    CHECK_NEAR(edge.g, 1.0, 1e-15);
    CHECK_NEAR(edge.b, 1.0, 1e-15);

    // Offset domain, including a negative minimum.
    const LUT shifted = identityLut(5, Domain{{-1.0, 0.0, 0.5}, {1.0, 2.0, 2.5}});
    // Identity LUT over an offset domain must reproduce the input exactly.
    const RGB mapped = applyLut(shifted, {0.0, 1.0, 1.5}, BoundaryPolicy::Reject);
    CHECK_NEAR(mapped.r, 0.0, 1e-14);
    CHECK_NEAR(mapped.g, 1.0, 1e-14);
    CHECK_NEAR(mapped.b, 1.5, 1e-14);
}

}  // namespace

int main() {
    testParserAcceptance();
    testParserRejections();
    testInterpolation();
    if (g_failures != 0) {
        std::cerr << g_failures << " check(s) failed\n";
        return 1;
    }
    std::cout << "all pipeline checks passed\n";
    return 0;
}
