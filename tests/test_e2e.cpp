#include <cstdio>
#include <filesystem>
#include <fstream>
#include <string>
#include <unistd.h>

#include "grade/cli.hpp"
#include "grade/cube_lut.hpp"
#include "grade/error.hpp"
#include "grade/fixture.hpp"
#include "grade/image_ppm.hpp"
#include "grade/render.hpp"

#include "framework.hpp"

using namespace grade;

namespace {

std::string uniqueTempDir() {
    return "/tmp/lutgrade_e2e_" + std::to_string(::getpid());
}

RenderConfig baseConfig() {
    RenderConfig config;
    config.inputSpace = ColorSpaceId::srgb;
    config.lutSpace = ColorSpaceId::displayP3;
    config.outputSpace = ColorSpaceId::srgb;
    config.interpolation = InterpId::tetrahedral;
    config.threads = 4;
    return config;
}

uint64_t histogramSum(const Histogram& histogram) {
    uint64_t total = 0;
    for (uint64_t count : histogram) {
        total += count;
    }
    return total;
}

} // namespace

GRADE_TEST(e2e_fixture_generates_parseable_artifacts) {
    std::string dir = uniqueTempDir() + "/fixture";
    FixtureOptions options;
    options.dir = dir;
    options.width = 24;
    options.height = 16;
    options.grid = 11;
    FixtureArtifacts artifacts = generateFixtures(options);

    CubeLut lut = parseCubeFile(artifacts.lutPath);
    GRADE_REQUIRE(lut.gridSize() == 11);
    GRADE_REQUIRE(lut.data().size() == 11u * 11u * 11u);
    PpmImage input = readPpmFile(artifacts.inputPath);
    GRADE_REQUIRE(input.width() == 24 && input.height() == 16);
    GRADE_REQUIRE(input.maxval() == 65535);
    GRADE_REQUIRE(input.pixelCount() == 384u);
}

GRADE_TEST(e2e_full_pipeline_runs_serially_and_threaded) {
    std::string dir = uniqueTempDir() + "/pipeline";
    FixtureOptions options;
    options.dir = dir;
    options.width = 48;
    options.height = 32;
    options.grid = 17;
    FixtureArtifacts artifacts = generateFixtures(options);

    PpmImage input = readPpmFile(artifacts.inputPath);
    CubeLut lut = parseCubeFile(artifacts.lutPath);
    RenderConfig config = baseConfig();

    PpmImage reference(input.width(), input.height());
    RenderConfig serial = config;
    serial.threads = 1;
    RenderResult serialResult = renderSerial(input, lut, serial, reference);

    PpmImage threaded(input.width(), input.height());
    RenderResult threadedResult =
        renderParallel(input, lut, config, threaded, config.threads);

    auto deviation = maximumChannelDifference(reference, threaded);
    GRADE_REQUIRE(deviation[0] == 0.0);
    GRADE_REQUIRE(deviation[1] == 0.0);
    GRADE_REQUIRE(deviation[2] == 0.0);
    GRADE_REQUIRE(serialResult.lutGrid == 17);
    GRADE_REQUIRE(serialResult.pixels == input.pixelCount());
    GRADE_REQUIRE(threadedResult.outOfBounds == serialResult.outOfBounds);

    for (const Histogram* histogram :
         {&serialResult.before.r, &serialResult.before.g,
          &serialResult.before.b, &serialResult.after.r,
          &serialResult.after.g, &serialResult.after.b}) {
        GRADE_REQUIRE(histogramSum(*histogram) == input.pixelCount());
    }

    std::string outputPath = dir + "/graded.ppm";
    writePpmFileAtomic(outputPath, threaded);
    PpmImage reread = readPpmFile(outputPath);
    GRADE_REQUIRE(reread.width() == input.width());
    GRADE_REQUIRE(reread.height() == input.height());
}

GRADE_TEST(e2e_both_interpolators_produce_valid_output) {
    std::string dir = uniqueTempDir() + "/interp";
    FixtureOptions options;
    options.dir = dir;
    options.width = 10;
    options.height = 10;
    options.grid = 8;
    FixtureArtifacts artifacts = generateFixtures(options);
    PpmImage input = readPpmFile(artifacts.inputPath);
    CubeLut lut = parseCubeFile(artifacts.lutPath);

    for (auto interpolation :
         {InterpId::trilinear, InterpId::tetrahedral}) {
        RenderConfig config = baseConfig();
        config.interpolation = interpolation;
        config.lutSpace = ColorSpaceId::srgb;
        PpmImage output(input.width(), input.height());
        RenderResult result = renderImage(input, lut, config, output);
        GRADE_REQUIRE(result.pixels == input.pixelCount());
        for (const auto& pixel : output.pixels()) {
            for (double sample : pixel) {
                GRADE_REQUIRE(std::isfinite(sample));
                GRADE_REQUIRE(sample >= 0.0 && sample <= 1.0);
            }
        }
    }
}

GRADE_TEST(e2e_out_of_domain_pixels_are_counted_or_rejected) {
    std::string dir = uniqueTempDir() + "/oob";
    FixtureOptions options;
    options.dir = dir;
    options.width = 4;
    options.height = 1;
    options.grid = 4;
    FixtureArtifacts artifacts = generateFixtures(options);
    CubeLut lut = parseCubeFile(artifacts.lutPath);

    PpmImage wide(4, 1);
    wide.at(0) = {{1.0, 1.0, 1.0}};
    wide.at(1) = {{0.0, 0.0, 0.0}};
    wide.at(2) = {{1.0, 0.0, 0.0}};
    wide.at(3) = {{0.0, 0.0, 1.0}};

    RenderConfig config = baseConfig();
    config.lutSpace = ColorSpaceId::displayP3;
    PpmImage clamped(4, 1);
    RenderResult result = renderSerial(wide, lut, config, clamped);
    // The saturated sRGB blue exceeds the Display-P3 LUT domain.
    GRADE_REQUIRE(result.outOfBounds >= 1u);

    RenderConfig strict = config;
    strict.outOfBounds = OobId::error;
    PpmImage rejected(4, 1);
    GRADE_REQUIRE_THROWS(renderSerial(wide, lut, strict, rejected),
                         RenderError);
}

GRADE_TEST(e2e_corrupted_lut_and_image_are_rejected) {
    std::string dir = uniqueTempDir() + "/corrupt";
    FixtureOptions options;
    options.dir = dir;
    options.width = 4;
    options.height = 4;
    options.grid = 4;
    FixtureArtifacts artifacts = generateFixtures(options);

    std::string badLutPath = dir + "/broken.cube";
    std::ofstream badLut(badLutPath);
    badLut << "LUT_3D_SIZE 4\n1 2 3\n";
    badLut.close();
    GRADE_REQUIRE_THROWS(parseCubeFile(badLutPath), FormatError);

    std::string badImagePath = dir + "/broken.ppm";
    std::ofstream badImage(badImagePath, std::ios::binary);
    badImage << "P6\n4 4\n255\n" << std::string(10, '\0');
    badImage.close();
    GRADE_REQUIRE_THROWS(readPpmFile(badImagePath), FormatError);

    GRADE_REQUIRE_THROWS(parseCubeFile(dir + "/missing.cube"), IoError);
}

GRADE_TEST(e2e_failed_render_leaves_no_output) {
    std::string dir = uniqueTempDir() + "/nooutput";
    FixtureOptions options;
    options.dir = dir;
    options.width = 2;
    options.height = 2;
    options.grid = 4;
    FixtureArtifacts artifacts = generateFixtures(options);
    CubeLut lut = parseCubeFile(artifacts.lutPath);

    PpmImage input(2, 2);
    for (auto& pixel : input.pixels()) {
        pixel = {{2.0, 0.0, 0.0}};
    }
    RenderConfig config = baseConfig();
    config.outOfBounds = OobId::error;
    PpmImage output(2, 2);
    std::string target = dir + "/should_not_exist.ppm";
    bool threw = false;
    try {
        renderImage(input, lut, config, output);
        writePpmFileAtomic(target, output);
    } catch (const RenderError&) {
        threw = true;
    }
    GRADE_REQUIRE(threw);
    GRADE_REQUIRE(!std::filesystem::exists(target));
}

int main() {
    int status = gradetest::runAll();
    std::filesystem::remove_all(uniqueTempDir());
    return status;
}
