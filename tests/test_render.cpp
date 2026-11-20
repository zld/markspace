#include <cstdint>
#include <cstdio>
#include <fstream>
#include <string>
#include <unistd.h>
#include <vector>

#include "grade/cube_lut.hpp"
#include "grade/error.hpp"
#include "grade/image_ppm.hpp"
#include "grade/render.hpp"

#include "framework.hpp"

using namespace grade;

namespace {

std::vector<uint8_t> rawBytes(const std::string& text) {
    return std::vector<uint8_t>(text.begin(), text.end());
}

std::string identityCube(int grid) {
    std::string text = "LUT_3D_SIZE " + std::to_string(grid) + "\n";
    for (int b = 0; b < grid; ++b) {
        for (int g = 0; g < grid; ++g) {
            for (int r = 0; r < grid; ++r) {
                text += std::to_string(static_cast<double>(r) / (grid - 1)) +
                        " " +
                        std::to_string(static_cast<double>(g) / (grid - 1)) +
                        " " +
                        std::to_string(static_cast<double>(b) / (grid - 1)) +
                        "\n";
            }
        }
    }
    return text;
}

std::string gradedCube(int grid) {
    // Bounded non-identity look: gain below 1 plus a small offset.
    std::string text = "LUT_3D_SIZE " + std::to_string(grid) + "\n";
    for (int b = 0; b < grid; ++b) {
        for (int g = 0; g < grid; ++g) {
            for (int r = 0; r < grid; ++r) {
                double rv = 0.9 * static_cast<double>(r) / (grid - 1) + 0.05;
                double gv = 1.1 * static_cast<double>(g) / (grid - 1);
                double bv = 0.8 * static_cast<double>(b) / (grid - 1) + 0.1;
                text += std::to_string(rv) + " " + std::to_string(gv) + " " +
                        std::to_string(bv) + "\n";
            }
        }
    }
    return text;
}

PpmImage makeGradient(int width, int height) {
    PpmImage image(width, height);
    for (int y = 0; y < height; ++y) {
        for (int x = 0; x < width; ++x) {
            size_t index = static_cast<size_t>(y) *
                               static_cast<size_t>(width) +
                           static_cast<size_t>(x);
            image.at(index) = {{static_cast<double>(x) / width,
                                static_cast<double>(y) / height,
                                0.5 + 0.25 * static_cast<double>(
                                                  (x + y) % 4) / 3.0}};
        }
    }
    return image;
}

} // namespace

// --- PPM ------------------------------------------------------------------

GRADE_TEST(ppm_reads_8bit_with_comments) {
    std::vector<uint8_t> bytes =
        rawBytes("P6\n# comment\n2 1\n# another\n255\n");
    bytes.insert(bytes.end(), {255, 0, 0, 0, 0, 255});
    PpmImage image = parsePpmBytes(bytes);
    GRADE_REQUIRE(image.width() == 2 && image.height() == 1);
    GRADE_REQUIRE(image.maxval() == 255);
    GRADE_REQUIRE_NEAR(image.at(0)[0], 1.0, 1.0e-15);
    GRADE_REQUIRE_NEAR(image.at(1)[2], 1.0, 1.0e-15);
}

GRADE_TEST(ppm_reads_16bit_big_endian) {
    std::vector<uint8_t> bytes = rawBytes("P6\n1 1\n1023\n");
    bytes.insert(bytes.end(), {0x01, 0x00, 0x03, 0xFF, 0x00, 0x10});
    PpmImage image = parsePpmBytes(bytes);
    GRADE_REQUIRE_NEAR(image.at(0)[0], 256.0 / 1023.0, 1.0e-15);
    GRADE_REQUIRE_NEAR(image.at(0)[1], 1.0, 1.0e-15);
}

GRADE_TEST(ppm_rejects_bad_magic_maxval_and_truncation) {
    GRADE_REQUIRE_THROWS(parsePpmBytes(rawBytes("P3\n1 1\n255\n")),
                         FormatError);
    GRADE_REQUIRE_THROWS(parsePpmBytes(rawBytes("P6\n1 1\n0\n")),
                         FormatError);
    GRADE_REQUIRE_THROWS(parsePpmBytes(rawBytes("P6\n1 1\n65536\n")),
                         FormatError);
    GRADE_REQUIRE_THROWS(
        parsePpmBytes(rawBytes("P6\n2 1\n255\n" + std::string(3, '\0'))),
        FormatError);
}

GRADE_TEST(ppm_atomic_write_round_trips_and_cleans_temp) {
    std::string path =
        "/tmp/lutgrade_ppm_test_" + std::to_string(::getpid()) + ".ppm";
    std::remove(path.c_str());
    PpmImage original(2, 2);
    original.setMaxval(1023);
    original.at(3) = {{0.25, 0.5, 0.75}};
    writePpmFileAtomic(path, original);
    PpmImage reread = readPpmFile(path);
    GRADE_REQUIRE(reread.width() == 2 && reread.maxval() == 1023);
    GRADE_REQUIRE_NEAR(reread.at(3)[1], 0.5, 1.0 / 1023.0);
    std::ifstream leftover(path + ".tmp." + std::to_string(::getpid()));
    GRADE_REQUIRE(!leftover.good());
    std::remove(path.c_str());
}

GRADE_TEST(ppm_missing_file_is_io_error) {
    GRADE_REQUIRE_THROWS(readPpmFile("/no/such/image.ppm"), IoError);
}

// --- interpolation kernel -------------------------------------------------

GRADE_TEST(render_nodes_are_exact_for_both_schemes) {
    CubeLut lut = parseCubeString(identityCube(5));
    uint64_t clamps = 0;
    for (int i = 0; i < 5; ++i) {
        double value = static_cast<double>(i) / 4.0;
        CubeLut::Rgb query{{value, value, value}};
        auto tri = sampleLut(lut, query, InterpId::trilinear,
                             OobId::clamp, clamps);
        auto tet = sampleLut(lut, query, InterpId::tetrahedral,
                             OobId::clamp, clamps);
        for (size_t channel = 0; channel < 3; ++channel) {
            GRADE_REQUIRE_NEAR(tri[channel], value, 1.0e-15);
            GRADE_REQUIRE_NEAR(tet[channel], value, 1.0e-15);
        }
    }
}

GRADE_TEST(render_tetrahedral_partitions_unity) {
    std::string text = "LUT_3D_SIZE 3\n";
    for (int entry = 0; entry < 27; ++entry) {
        text += "0.25 0.5 0.75\n";
    }
    CubeLut lut = parseCubeString(text);
    uint64_t clamps = 0;
    auto result = sampleLut(lut, CubeLut::Rgb{{0.37, 0.81, 0.12}},
                            InterpId::tetrahedral, OobId::clamp, clamps);
    GRADE_REQUIRE_NEAR(result[0], 0.25, 1.0e-15);
    GRADE_REQUIRE_NEAR(result[1], 0.5, 1.0e-15);
    GRADE_REQUIRE_NEAR(result[2], 0.75, 1.0e-15);
}

GRADE_TEST(render_clamp_counts_and_error_policy) {
    CubeLut lut = parseCubeString(identityCube(2));
    uint64_t clamps = 0;
    auto clamped = sampleLut(lut, CubeLut::Rgb{{1.7, -0.4, 0.5}},
                             InterpId::trilinear, OobId::clamp, clamps);
    GRADE_REQUIRE(clamps == 2u);
    GRADE_REQUIRE(clamped[0] == 1.0 && clamped[1] == 0.0);
    GRADE_REQUIRE_THROWS(
        sampleLut(lut, CubeLut::Rgb{{1.01, 0.0, 0.0}},
                  InterpId::trilinear, OobId::error, clamps),
        RenderError);
}

GRADE_TEST(render_rejects_non_finite_pixels) {
    CubeLut lut = parseCubeString(identityCube(2));
    uint64_t clamps = 0;
    GRADE_REQUIRE_THROWS(
        sampleLut(lut, CubeLut::Rgb{{NAN, 0.0, 0.0}},
                  InterpId::trilinear, OobId::clamp, clamps),
        RenderError);
}

GRADE_TEST(render_identity_lut_preserves_same_space_pixels) {
    CubeLut lut = parseCubeString(identityCube(9));
    PpmImage input(1, 1);
    input.at(0) = {{0.25, 0.5, 0.75}};
    PpmImage output(1, 1);
    RenderConfig config;
    RenderResult result = renderSerial(input, lut, config, output);
    for (size_t channel = 0; channel < 3; ++channel) {
        GRADE_REQUIRE_NEAR(output.at(0)[channel], input.at(0)[channel],
                           1.0e-12);
    }
    GRADE_REQUIRE(result.outOfBounds == 0u);
}

// --- serial vs parallel ---------------------------------------------------

GRADE_TEST(render_parallel_matches_serial_bit_for_bit) {
    constexpr int width = 37;
    constexpr int height = 19;
    PpmImage input = makeGradient(width, height);
    for (auto interpolation :
         {InterpId::trilinear, InterpId::tetrahedral}) {
        for (auto lutSpace :
             {ColorSpaceId::srgb, ColorSpaceId::displayP3}) {
            CubeLut lut = parseCubeString(gradedCube(9));
            RenderConfig config;
            config.interpolation = interpolation;
            config.lutSpace = lutSpace;
            PpmImage reference(width, height);
            renderSerial(input, lut, config, reference);
            for (int threads : {2, 3, 4, 7, 16}) {
                RenderConfig threaded = config;
                threaded.threads = threads;
                PpmImage parallel(width, height);
                renderParallel(input, lut, threaded, parallel, threads);
                auto diff = maximumChannelDifference(reference, parallel);
                GRADE_REQUIRE(diff[0] == 0.0);
                GRADE_REQUIRE(diff[1] == 0.0);
                GRADE_REQUIRE(diff[2] == 0.0);
            }
        }
    }
}

GRADE_TEST(render_histogram_totals_equal_pixel_count) {
    constexpr int width = 16;
    constexpr int height = 8;
    PpmImage input = makeGradient(width, height);
    CubeLut lut = parseCubeString(gradedCube(5));
    RenderConfig config;
    config.threads = 3;
    PpmImage output(width, height);
    RenderResult result =
        renderParallel(input, lut, config, output, config.threads);
    const Histogram* before[3] = {&result.before.r, &result.before.g,
                                  &result.before.b};
    const Histogram* after[3] = {&result.after.r, &result.after.g,
                                 &result.after.b};
    for (int channel = 0; channel < 3; ++channel) {
        uint64_t beforeSum = 0;
        uint64_t afterSum = 0;
        for (uint64_t count : *before[channel]) {
            beforeSum += count;
        }
        for (uint64_t count : *after[channel]) {
            afterSum += count;
        }
        GRADE_REQUIRE(beforeSum == input.pixelCount());
        GRADE_REQUIRE(afterSum == input.pixelCount());
    }
}

GRADE_TEST(render_worker_exception_is_propagated) {
    CubeLut lut = parseCubeString(identityCube(2));
    PpmImage input(1, 1);
    input.at(0) = {{2.0, 0.0, 0.0}};
    PpmImage output(1, 1);
    RenderConfig config;
    config.threads = 2;
    config.outOfBounds = OobId::error;
    GRADE_REQUIRE_THROWS(
        renderParallel(input, lut, config, output, config.threads),
        RenderError);
}

GRADE_TEST(render_thread_count_is_validated) {
    PpmImage input(1, 1);
    PpmImage output(1, 1);
    RenderConfig config;
    CubeLut lut = parseCubeString(identityCube(2));
    GRADE_REQUIRE_THROWS(renderParallel(input, lut, config, output, 0),
                         UsageError);
    GRADE_REQUIRE_THROWS(renderParallel(input, lut, config, output, 257),
                         UsageError);
}

int main() { return gradetest::runAll(); }
