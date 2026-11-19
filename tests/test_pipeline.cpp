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

void testColorSpaces() {
    for (double value = 0.0; value <= 1.0; value += 1.0 / 64.0) {
        CHECK_NEAR(linearToSrgb(srgbToLinear({value, value, value})).r, value, 1e-12);
    }
    CHECK_NEAR(srgbToLinear({0.04045, 0.04045, 0.04045}).r, 0.04045 / 12.92, 1e-15);
    CHECK_NEAR(srgbToLinear({0.5, 0.5, 0.5}).r, 0.21404114048223255, 1e-12);

    const RGB white{1.0, 1.0, 1.0};
    const RGB xyz = linearRgbToXyz(white);
    CHECK_NEAR(xyz.r, 0.950455927051671, 1e-9);
    CHECK_NEAR(xyz.g, 1.0, 1e-9);
    CHECK_NEAR(xyz.b, 1.089057750759878, 1e-9);
    const RGB back = xyzToLinearRgb(xyz);
    CHECK_NEAR(back.r, 1.0, 1e-9);
    CHECK_NEAR(back.g, 1.0, 1e-9);
    CHECK_NEAR(back.b, 1.0, 1e-9);

    Mat3 inverse;
    CHECK(invertMatrix(srgbToXyzMatrix(), inverse));
    const Mat3 identity = multiply(srgbToXyzMatrix(), inverse);
    for (int i = 0; i < 3; ++i) {
        for (int j = 0; j < 3; ++j) {
            CHECK_NEAR(identity(i, j), i == j ? 1.0 : 0.0, 1e-12);
        }
    }
    Mat3 singular;
    singular.e = {1, 2, 3, 2, 4, 6, 7, 8, 9};
    CHECK(!invertMatrix(singular, inverse));

    // srgb -> xyz -> srgb round trip.
    const RGB sample{0.2, 0.6, 0.9};
    const RGB viaXyz = convertColor(ColorSpace::Xyz, ColorSpace::Srgb,
                                    convertColor(ColorSpace::Srgb, ColorSpace::Xyz, sample));
    CHECK_NEAR(viaXyz.r, sample.r, 1e-9);
    CHECK_NEAR(viaXyz.g, sample.g, 1e-9);
    CHECK_NEAR(viaXyz.b, sample.b, 1e-9);

    bool threw = false;
    try {
        convertColor(ColorSpace::Srgb, ColorSpace::Xyz, {0.0, NAN, 0.0}, 42);
    } catch (const ColorError& error) {
        threw = std::string(error.what()).find("42") != std::string::npos;
    }
    CHECK(threw);
    CHECK(parseColorSpace("Linear-RGB") == ColorSpace::LinearRgb);
    bool badSpace = false;
    try {
        parseColorSpace("rec2020");
    } catch (const ColorError&) {
        badSpace = true;
    }
    CHECK(badSpace);
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

void testRenderPathsAndHistograms() {
    const LUT lut = makeDemoLut(9);
    const std::vector<RGB> pixels = makeDemoPixels(10007, ColorSpace::Srgb, 12345);
    const std::vector<RGB> working = [&] {
        std::vector<RGB> out;
        for (const RGB& pixel : pixels) {
            out.push_back(convertColor(ColorSpace::Srgb, ColorSpace::LinearRgb, pixel));
        }
        return out;
    }();
    const std::vector<RGB> scalar = renderScalar(lut, working, BoundaryPolicy::Clamp);
    for (int threads : {1, 2, 3, 7, 32}) {
        const std::vector<RGB> parallel =
            renderParallel(lut, working, BoundaryPolicy::Clamp, threads);
        const RGB diff = maxAbsDiff(scalar, parallel);
        CHECK_NEAR(diff.r, 0.0, 0.0);
        CHECK_NEAR(diff.g, 0.0, 0.0);
        CHECK_NEAR(diff.b, 0.0, 0.0);
    }
    // Partition covers every pixel exactly once, in order.
    const std::vector<Chunk> chunks = partition(10007, 4);
    CHECK(chunks.size() == 4);
    CHECK(chunks.front().begin == 0);
    CHECK(chunks.back().end == 10007);
    for (std::size_t i = 1; i < chunks.size(); ++i) CHECK(chunks[i].begin == chunks[i - 1].end);
    std::size_t total = 0;
    for (const Chunk& chunk : chunks) total += chunk.end - chunk.begin;
    CHECK(total == 10007);

    // Reject policy must fail without emitting a partial buffer; the error
    // reported must be the first one in pixel order.
    std::vector<RGB> bad = working;
    bad[500] = {5.0, 0.0, 0.0};
    bool failed = false;
    try {
        renderParallel(lut, bad, BoundaryPolicy::Reject, 8);
    } catch (const ProcessingError& error) {
        failed = std::string(error.what()).find("pixel 500") != std::string::npos;
    }
    CHECK(failed);

    const ChannelHistograms histogram = buildHistograms(working);
    CHECK(histogram.r.total() == working.size());
    CHECK(histogram.g.total() == working.size());
    CHECK(histogram.b.total() == working.size());
    CHECK(histogramBucket(-1.0) == 0);
    CHECK(histogramBucket(10.0) == 15);
    CHECK(histogramBucket(0.0) == 0);
    CHECK(histogramBucket(1.0) == 15);
    CHECK(histogramBucket(0.0625) == 1);
}

void testDeterminismAndReport() {
    const std::vector<RGB> first = makeDemoPixels(1024, ColorSpace::Srgb, 777);
    const std::vector<RGB> second = makeDemoPixels(1024, ColorSpace::Srgb, 777);
    CHECK(first.size() == second.size());
    bool identical = true;
    for (std::size_t i = 0; i < first.size(); ++i) {
        if (!(first[i] == second[i])) identical = false;
    }
    CHECK(identical);

    Report report;
    report.pixels = 4;
    report.before.r.buckets[0] = 4;
    report.before.g.buckets[0] = 4;
    report.before.b.buckets[0] = 4;
    report.after = report.before;
    CHECK(validateCounts(report).empty());
    report.after.b.buckets[0] = 3;
    CHECK(!validateCounts(report).empty());

    const std::string json = renderJson(report);
    CHECK(json.find("\"max_abs_diff\"") != std::string::npos);
    CHECK(json.find("histogram_after") != std::string::npos);
    const std::string text = renderText(report);
    CHECK(text.find("histogram_before") != std::string::npos);
    CHECK(!text.empty() && text.back() == '\n');
}

std::vector<char*> argvOf(std::vector<std::string>& storage) {
    std::vector<char*> argv;
    for (std::string& item : storage) argv.push_back(item.data());
    return argv;
}

void testOptions() {
    auto parse = [](std::vector<std::string> args) {
        std::vector<char*> argv = argvOf(args);
        return parseOptions(static_cast<int>(argv.size()), argv.data());
    };
    auto rejects = [&](std::vector<std::string> args) {
        try {
            parse(std::move(args));
        } catch (const std::invalid_argument&) {
            return true;
        }
        return false;
    };

    const Options defaults = parse({"lutcheck", "demo"});
    CHECK(defaults.size == 17);
    CHECK(defaults.pixels == 65536);
    CHECK(defaults.threads == 4);
    CHECK(defaults.inputSpace == ColorSpace::Srgb);
    CHECK(defaults.lutSpace == ColorSpace::LinearRgb);
    CHECK(defaults.boundary == BoundaryPolicy::Clamp);

    const Options custom = parse({"lutcheck", "demo", "--size", "33", "--pixels", "10",
                                  "--threads", "2", "--lut-space", "xyz", "--boundary",
                                  "reject", "--format", "json", "--seed", "99"});
    CHECK(custom.size == 33);
    CHECK(custom.pixels == 10);
    CHECK(custom.threads == 2);
    CHECK(custom.lutSpace == ColorSpace::Xyz);
    CHECK(custom.boundary == BoundaryPolicy::Reject);
    CHECK(custom.format == OutputFormat::Json);
    CHECK(custom.seed == 99);

    CHECK(parse({"lutcheck", "--help"}).requestHelp);
    CHECK(rejects({"lutcheck"}));
    CHECK(rejects({"lutcheck", "render"}));
    CHECK(rejects({"lutcheck", "demo", "--size", "1"}));
    CHECK(rejects({"lutcheck", "demo", "--size", "66"}));
    CHECK(rejects({"lutcheck", "demo", "--pixels", "0"}));
    CHECK(rejects({"lutcheck", "demo", "--pixels", "16777217"}));
    CHECK(rejects({"lutcheck", "demo", "--threads", "0"}));
    CHECK(rejects({"lutcheck", "demo", "--threads", "33"}));
    CHECK(rejects({"lutcheck", "demo", "--seed", "-3"}));
    CHECK(rejects({"lutcheck", "demo", "--size"}));
    CHECK(rejects({"lutcheck", "demo", "--input-space", "xyz"}));
    CHECK(rejects({"lutcheck", "demo", "--lut-order", "bgr", "--lut-file", "x.cube"}));
    CHECK(rejects({"lutcheck", "demo", "--bogus"}));
}

}  // namespace

int main() {
    testParserAcceptance();
    testParserRejections();
    testColorSpaces();
    testInterpolation();
    testRenderPathsAndHistograms();
    testDeterminismAndReport();
    testOptions();
    if (g_failures != 0) {
        std::cerr << g_failures << " check(s) failed\n";
        return 1;
    }
    std::cout << "all pipeline checks passed\n";
    return 0;
}
