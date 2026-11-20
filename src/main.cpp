#include <array>
#include <cstdio>
#include <iostream>
#include <string>

#include "grade/cli.hpp"
#include "grade/cube_lut.hpp"
#include "grade/error.hpp"
#include "grade/fixture.hpp"
#include "grade/image_ppm.hpp"
#include "grade/render.hpp"

namespace {

using grade::ChannelHistograms;
using grade::RenderConfig;
using grade::RenderResult;

int handleError(const grade::GradeError& error) {
    std::cerr << "lutgrade: " << error.what() << '\n';
    return static_cast<int>(error.code());
}

RenderConfig toRenderConfig(const grade::RenderOptions& options) {
    RenderConfig config;
    config.inputSpace = options.inputSpace;
    config.lutSpace = options.lutSpace;
    config.outputSpace = options.outputSpace;
    config.interpolation = options.interpolation;
    config.outOfBounds = options.outOfBounds;
    config.threads = options.threads;
    return config;
}

void printHistogram(const char* label, const ChannelHistograms& hist) {
    const grade::Histogram* channels[3] = {&hist.r, &hist.g, &hist.b};
    const char* names[3] = {"r", "g", "b"};
    for (int channel = 0; channel < 3; ++channel) {
        std::cout << "histogram_" << label << '_' << names[channel] << '=';
        for (size_t bin = 0; bin < grade::kHistogramBins; ++bin) {
            std::cout << ' ' << (*channels[channel])[bin];
        }
        std::cout << '\n';
    }
}

void printSummary(const RenderResult& result) {
    std::cout << "lut_grid=" << result.lutGrid << '\n';
    std::cout << "pixels=" << result.pixels << '\n';
    std::cout << "threads=" << result.threads << '\n';
    std::cout << "out_of_bounds=" << result.outOfBounds << '\n';
    printHistogram("before", result.before);
    printHistogram("after", result.after);
}

int runFixture(const grade::FixtureOptions& options) {
    grade::FixtureArtifacts artifacts = grade::generateFixtures(options);
    std::cout << "fixture_dir=" << options.dir << '\n';
    std::cout << "fixture_input=" << artifacts.inputPath << '\n';
    std::cout << "fixture_lut=" << artifacts.lutPath << '\n';
    std::cout << "fixture_patches=" << artifacts.patchesPath << '\n';
    return static_cast<int>(grade::ExitCode::ok);
}

int runRender(const grade::RenderOptions& options) {
    grade::PpmImage input = grade::readPpmFile(options.input);
    grade::CubeLut lut = grade::parseCubeFile(options.lut);
    RenderConfig config = toRenderConfig(options);

    grade::PpmImage output(input.width(), input.height());
    output.setMaxval(input.maxval());

    RenderResult result;
    if (options.path == grade::PathMode::compare) {
        // Shared kernel, two schedules: serial reference plus the
        // requested threaded schedule, then a full float comparison.
        RenderConfig serialConfig = config;
        serialConfig.threads = 1;
        grade::PpmImage reference(input.width(), input.height());
        reference.setMaxval(input.maxval());
        RenderResult serialResult =
            grade::renderSerial(input, lut, serialConfig, reference);

        grade::PpmImage parallel(input.width(), input.height());
        parallel.setMaxval(input.maxval());
        RenderConfig parallelConfig = config;
        parallelConfig.threads = options.threads;
        result = grade::renderParallel(input, lut, parallelConfig, parallel,
                                       options.threads);

        std::array<double, 3> deviation =
            grade::maximumChannelDifference(reference, parallel);
        std::printf("max_dev_r=%.6f\n", deviation[0]);
        std::printf("max_dev_g=%.6f\n", deviation[1]);
        std::printf("max_dev_b=%.6f\n", deviation[2]);
        output = std::move(parallel);
        // Schedules share every counter; keep the reported stats honest.
        result.before = serialResult.before;
        result.after = serialResult.after;
        result.outOfBounds = serialResult.outOfBounds;
    } else {
        result = grade::renderImage(input, lut, config, output);
    }

    // The file is only touched after every pixel has been produced, and
    // the write itself is temp-file + rename, so no failure can leave a
    // complete output behind.
    grade::writePpmFileAtomic(options.output, output);
    std::cout << "output=" << options.output << '\n';
    std::cout << "interpolation=" << grade::toString(options.interpolation)
              << '\n';
    std::cout << "input_space=" << grade::toString(options.inputSpace)
              << '\n';
    std::cout << "lut_space=" << grade::toString(options.lutSpace) << '\n';
    std::cout << "output_space=" << grade::toString(options.outputSpace)
              << '\n';
    printSummary(result);
    return static_cast<int>(grade::ExitCode::ok);
}

} // namespace

int main(int argc, char** argv) {
    try {
        grade::Arguments args = grade::parseCommandLine(argc - 1, argv + 1);
        if (args.command == grade::Command::fixture) {
            return runFixture(args.fixture);
        }
        return runRender(args.render);
    } catch (const grade::GradeError& error) {
        return handleError(error);
    } catch (const std::exception& error) {
        std::cerr << "lutgrade: unexpected error: " << error.what() << '\n';
        return static_cast<int>(grade::ExitCode::render);
    }
}
