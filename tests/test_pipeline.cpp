#include <cmath>
#include <fstream>
#include <string>

#include "lut/baker.hpp"
#include "lut/color_space.hpp"
#include "lut/cube_reader.hpp"
#include "lut/fixtures.hpp"
#include "lut/image.hpp"
#include "lut/interpolator.hpp"
#include "lut/model.hpp"
#include "lut/report.hpp"
#include "lut/render.hpp"
#include "test_helpers.hpp"

using namespace lut;

namespace {

void writeFile(const std::string& path, const std::string& bytes) {
    std::ofstream f(path, std::ios::binary);
    f.write(bytes.data(), static_cast<std::streamsize>(bytes.size()));
}

Lut3D identityLut(int n) {
    Lut3D l;
    l.size = n;
    l.nodes.resize(l.nodeCount());
    for (int b = 0; b < n; ++b)
        for (int g = 0; g < n; ++g)
            for (int r = 0; r < n; ++r)
                l.at(r, g, b) =
                    RGB{(double)r / (n - 1), (double)g / (n - 1),
                        (double)b / (n - 1)};
    return l;
}

// Matrix round trips, white-point invariance and transfer encode/decode.
void testColorSpaceRoundTrip() {
    ColorSpace s(ColorSpaceId::Srgb), p(ColorSpaceId::DisplayP3),
        r(ColorSpaceId::Rec709);
    for (const ColorSpace* cs : {&s, &p, &r}) {
        Mat3 rt = multiply(cs->xyzToRgb(), cs->rgbToXyz());
        for (int i = 0; i < 3; ++i)
            for (int j = 0; j < 3; ++j)
                CHECK_NEAR(rt[i][j], i == j ? 1.0 : 0.0, 1e-10);
        RGB white = s.convertLinear(RGB{1, 1, 1}, *cs);
        CHECK_NEAR(white.r, 1.0, 1e-10);
        CHECK_NEAR(white.g, 1.0, 1e-10);
        CHECK_NEAR(white.b, 1.0, 1e-10);
        for (double v = 0.0; v <= 1.0001; v += 0.07)
            CHECK_NEAR(cs->encodeTransfer(cs->decodeTransfer(v)), v, 1e-12);
    }
}

// BOM, CRLF, comments and red-fast node ordering through a real file.
void testCubeParsing() {
    const std::string dir = luttest::scratchDir();
    const std::string path = dir + "/sample.cube";
    std::string text = "\xEF\xBB\xBF# comment\r\nLUT_3D_SIZE 2\r\n";
    text += "DOMAIN_MIN 0 0 0\r\nDOMAIN_MAX 1 1 1\r\n";
    for (int b = 0; b < 2; ++b)
        for (int g = 0; g < 2; ++g)
            for (int r = 0; r < 2; ++r)
                text += std::to_string(r * 0.1) + " " +
                        std::to_string(g * 0.2) + " " +
                        std::to_string(b * 0.3) + "\r\n";
    writeFile(path, text);

    Lut3D lut = readCube(path);
    CHECK(lut.size == 2);
    CHECK(lut.nodes.size() == 8);
    // Red-fast: consecutive nodes advance the red axis first.
    CHECK(lut.at(1, 0, 0).r > lut.at(0, 0, 0).r);
    CHECK(lut.at(0, 1, 0).g > lut.at(0, 0, 0).g);
    CHECK(lut.at(0, 0, 1).b > lut.at(0, 0, 0).b);

    writeCube(dir + "/rt.cube", lut);
    Lut3D back = readCube(dir + "/rt.cube");
    CHECK(back.nodes == lut.nodes);
}

// Every malformed input must report a line number or a structured error.
void testCubeParseErrors() {
    const std::string dir = luttest::scratchDir();
    auto expectParse = [&](const std::string& body, int wantLine) {
        const std::string p = dir + "/bad.cube";
        writeFile(p, body);
        bool caught = false;
        try {
            readCube(p);
        } catch (const ParseError& e) {
            caught = true;
            CHECK(e.line() == wantLine);
        }
        CHECK(caught);
    };
    auto expectData = [&](const std::string& body) {
        const std::string p = dir + "/bad2.cube";
        writeFile(p, body);
        bool caught = false;
        try {
            readCube(p);
        } catch (const LutDataError&) {
            caught = true;
        }
        CHECK(caught);
    };
    expectParse("LUT_3D_SIZE 2\nLUT_3D_SIZE 3\n", 2);
    expectParse("LUT_1D_SIZE 4\n", 1);
    expectParse("LUT_3D_SIZE 66\n", 1);
    expectParse("LUT_3D_SIZE 2 garbage\n", 1);
    expectParse("LUT_3D_SIZE 2\nDOMAIN_MIN 0 0 0 junk\n", 2);
    expectParse("LUT_3D_SIZE 2\nBOGUS 1 2\n", 2);
    expectData("LUT_3D_SIZE 2\n0.1 0.2 0.3\n");  // Missing nodes.
}

// Boundary samples, identity exactness and domain policy switching.
void testInterpolatorBoundaries() {
    Lut3D lut = identityLut(4);
    Interpolator clamp(lut, DomainPolicy::Clamp);
    Interpolator strict(lut, DomainPolicy::Error);
    for (double v : {0.0, 1.0, 0.5}) {
        RGB out = clamp.sample(RGB{v, v, v});
        CHECK_NEAR(out.r, v, 1e-12);
    }
    RGB over = clamp.sample(RGB{1.5, -1.0, 0.5});
    CHECK_NEAR(over.r, 1.0, 1e-12);
    CHECK_NEAR(over.g, 0.0, 1e-12);
    bool threw = false;
    try {
        strict.sample(RGB{0.5, 1.01, 0.5});
    } catch (const LutDataError&) {
        threw = true;
    }
    CHECK(threw);
}

// P6 round trips at both bit depths and every malformed-file rejection.
void testPpmRoundTripAndErrors() {
    Image img;
    img.width = 3;
    img.height = 2;
    img.maxval = 255;
    img.pixels = {{0, 0, 0}, {0.25, 0.5, 0.75}, {1, 1, 1},
                  {0.1, 0.2, 0.3}, {0.4, 0.6, 0.8}, {0.9, 0.5, 0.1}};
    const std::string dir = luttest::scratchDir();
    writePpm(dir + "/a.ppm", img);
    Image back = readPpm(dir + "/a.ppm");
    CHECK(back.width == 3 && back.height == 2 && back.maxval == 255);
    for (std::size_t i = 0; i < img.pixels.size(); ++i)
        CHECK_NEAR(back.pixels[i].r, img.pixels[i].r, 1.0 / 255.0);

    Image wide = img;
    wide.maxval = 1023;
    writePpm(dir + "/b.ppm", wide);
    Image back16 = readPpm(dir + "/b.ppm");
    CHECK(back16.maxval == 1023);
    for (std::size_t i = 0; i < wide.pixels.size(); ++i)
        CHECK_NEAR(back16.pixels[i].g, wide.pixels[i].g, 1.0 / 1023.0);

    // Legal headers using CRLF separators and a comment after maxval must
    // resolve to the same raster as a plain LF header (regression guard).
    {
        const std::string raster =
            renderPpm(img).substr(renderPpm(img).find("255\n") + 4);
        writeFile(dir + "/crlf.ppm", "P6\r\n3 2\r\n255\r\n" + raster);
        Image fromCrlf = readPpm(dir + "/crlf.ppm");
        CHECK(fromCrlf.pixels == back.pixels);

        writeFile(dir + "/comment.ppm",
                  "P6\n3 2\n# note\n255\n# tail\n" + raster);
        Image fromComment = readPpm(dir + "/comment.ppm");
        CHECK(fromComment.pixels == back.pixels);
    }

    auto expectIo = [&](const std::string& bytes) {
        const std::string p = dir + "/bad.ppm";
        writeFile(p, bytes);
        bool caught = false;
        try {
            readPpm(p);
        } catch (const IoError&) {
            caught = true;
        }
        CHECK(caught);
    };
    expectIo("P3\n1 1\n255\n0 0 0\n");
    expectIo("P6\n0 1\n255\nxxx");
    expectIo("P6\n1 1\n0\nxx");
    expectIo("P6\n1 1\n70000\nxxxxxx");
    expectIo("P6\n2 2\n255\nabc");
    std::string trailing = "P6\n1 1\n255\n";
    trailing += std::string(3, 'x') + "y";
    expectIo(trailing);
}

// Histogram bucket totals and the before/after counters on real fixtures.
void testHistogramTotals() {
    Image patches = makeFixtureImage();
    CHECK(patches.pixelCount() == 4096);
    Lut3D lut = makeFixtureLut();
    RenderResult result =
        renderImage(lut, patches, DomainPolicy::Clamp, 16);
    for (int c = 0; c < 3; ++c) {
        CHECK(result.stats.beforeTotal(c) == 4096);
        CHECK(result.stats.afterTotal(c) == 4096);
    }
    CHECK(result.stats.outOfDomain == 0);

    Histogram hist{};
    accumulateHistogram(hist, 0.0);
    accumulateHistogram(hist, 1.0);
    CHECK(hist[0] == 1 && hist[15] == 1);
}

// The probe path and the texture path must share one kernel, so identical
// inputs produce identical outputs; tile geometry must not change pixels.
void testTwoPathEquivalence() {
    Lut3D lut = makeFixtureLut();
    Image patches = makeFixtureImage();
    RenderResult tiled16 = renderImage(lut, patches, DomainPolicy::Clamp, 16);
    RenderResult tiled7 = renderImage(lut, patches, DomainPolicy::Clamp, 7);
    CHECK(tiled16.graded.pixels == tiled7.graded.pixels);

    Interpolator interpolator(lut, DomainPolicy::Clamp);
    for (std::size_t i = 0; i < patches.pixels.size(); i += 131) {
        RGB viaProbe = renderProbe(interpolator, patches.pixels[i]);
        RGB viaImage = tiled16.graded.pixels[i];
        CHECK(viaProbe == viaImage);
    }
}

// Baking stays inside the error budget and same-space rebaking is near exact.
void testBakeDeviation() {
    Lut3D source = makeFixtureLut();
    ColorSpace srgb(ColorSpaceId::Srgb);
    ColorSpace p3(ColorSpaceId::DisplayP3);
    BakeResult cross = bakeLut(source, srgb, p3, 33, 17,
                               DomainPolicy::Clamp);
    CHECK(cross.baked.size == 33);
    CHECK(cross.probeCount == 4913u);
    CHECK(cross.deviation.worst() <= 0.02);

    // Ground-truth cross-space check, reconstructed independently of
    // gradeOnce. For a non-identity LUT a naive node-copy / half-conversion
    // diverges here (~1e-2), while the full decode/convert/sample/convert/
    // encode round trip matches to machine precision. This is the regression
    // guard against transplanting RGB values between colour spaces.
    {
        const int n = 33;
        BakeResult truth = bakeLut(source, srgb, p3, n, n,
                                   DomainPolicy::Clamp);
        Interpolator bakedSampler(truth.baked, DomainPolicy::Clamp);
        Interpolator sourceSampler2(source, DomainPolicy::Clamp);
        double worstGroundTruth = 0.0;
        for (int k = 0; k < n; ++k) {
            const double t = static_cast<double>(k) / (n - 1);
            const RGB targetEncoded{t, t, t};
            const RGB sourceEncoded = srgb.encode(
                p3.convertLinear(p3.decode(targetEncoded), srgb));
            const RGB gradedSource = sourceSampler2.sample(sourceEncoded);
            const RGB truthLinear =
                srgb.convertLinear(srgb.decode(gradedSource), p3);
            const RGB gotLinear =
                p3.decode(bakedSampler.sample(targetEncoded));
            worstGroundTruth = std::max(
                {worstGroundTruth, std::fabs(truthLinear.r - gotLinear.r),
                 std::fabs(truthLinear.g - gotLinear.g),
                 std::fabs(truthLinear.b - gotLinear.b)});
        }
        CHECK(worstGroundTruth < 1e-9);
    }

    BakeResult same = bakeLut(source, srgb, srgb, source.size, 17,
                              DomainPolicy::Clamp);
    CHECK(same.deviation.worst() < 1e-9);
}

// Out-of-domain pixels are counted under clamp and rejected under error.
void testDomainPoliciesInRender() {
    Lut3D lut = makeFixtureLut();
    Image patches = makeFixtureImage();
    patches.pixels[0] = RGB{1.2, 0.0, 0.0};
    RenderResult clamped = renderImage(lut, patches, DomainPolicy::Clamp, 8);
    CHECK(clamped.stats.outOfDomain == 1);

    bool threw = false;
    try {
        renderImage(lut, patches, DomainPolicy::Error, 8);
    } catch (const LutDataError&) {
        threw = true;
    }
    CHECK(threw);
}

// Clip and out-of-domain counters share a pixel unit: one pixel with several
// offending channels increments each counter exactly once.
void testCounterUnits() {
    Lut3D lut = makeFixtureLut();
    for (RGB& node : lut.nodes) {
        node.r += 0.5;  // Push many red outputs above 1.0.
        node.g += 0.5;
    }
    Image patches = makeFixtureImage();
    RenderResult result = renderImage(lut, patches, DomainPolicy::Clamp, 16);
    CHECK(result.stats.clipped <= patches.pixelCount());
    CHECK(result.stats.outOfDomain <= patches.pixelCount());

    // One in-range image with a single multi-channel out-of-domain input.
    Image single = makeFixtureImage();
    single.pixels[0] = RGB{1.3, -0.4, 0.5};
    RenderResult one = renderImage(lut, single, DomainPolicy::Clamp, 16);
    CHECK(one.stats.outOfDomain == 1);
}

// --patch-space must shape the encoded samples: sRGB and Display-P3 share a
// transfer, while Rec.709 uses a different one; all values stay in range.
void testFixturePatchSpace() {
    Image srgb = makeFixtureImage(ColorSpaceId::Srgb);
    Image p3 = makeFixtureImage(ColorSpaceId::DisplayP3);
    Image rec709 = makeFixtureImage(ColorSpaceId::Rec709);
    double srgbP3 = 0.0;
    double srgbRec = 0.0;
    for (std::size_t i = 0; i < srgb.pixels.size(); ++i) {
        srgbP3 = std::max(srgbP3,
                          std::fabs(srgb.pixels[i].r - p3.pixels[i].r));
        srgbRec = std::max(srgbRec,
                           std::fabs(srgb.pixels[i].r - rec709.pixels[i].r));
        for (const Image* im : {&srgb, &p3, &rec709}) {
            for (const RGB& px : im->pixels) {
                CHECK(px.r >= 0.0 && px.r <= 1.0);
            }
        }
    }
    CHECK(srgbP3 < 1e-12);  // Same transfer function.
    CHECK(srgbRec > 1e-3);  // Different transfer function.
}

}  // namespace

int main() {
    RUN_TEST(testColorSpaceRoundTrip);
    RUN_TEST(testCubeParsing);
    RUN_TEST(testCubeParseErrors);
    RUN_TEST(testInterpolatorBoundaries);
    RUN_TEST(testPpmRoundTripAndErrors);
    RUN_TEST(testHistogramTotals);
    RUN_TEST(testTwoPathEquivalence);
    RUN_TEST(testBakeDeviation);
    RUN_TEST(testDomainPoliciesInRender);
    RUN_TEST(testCounterUnits);
    RUN_TEST(testFixturePatchSpace);
    return luttest::finish();
}
