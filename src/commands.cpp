#include <iostream>

#include "lut/baker.hpp"
#include "lut/cli_internal.hpp"
#include "lut/color_space.hpp"
#include "lut/cube_reader.hpp"
#include "lut/fixtures.hpp"
#include "lut/image.hpp"
#include "lut/interpolator.hpp"
#include "lut/render.hpp"

namespace lut {

int cmdFixtureGen(const Options& options) {
    const std::string outDir = requireOption(options, "out");
    const auto spaceIt = options.find("patch-space");
    const ColorSpaceId space =
        spaceIt == options.end() ? ColorSpaceId::Srgb
                                 : parseColorSpace(spaceIt->second);
    writeFixtures(outDir, space);
    std::cout << "status=ok\n"
              << "command=fixture-gen\n"
              << "patch_space=" << name(space) << '\n'
              << "cube=" << outDir << "/source.cube\n"
              << "patches=" << outDir << "/patches.ppm\n";
    return kOk;
}

int cmdProbe(const Options& options) {
    const std::string cubePath = requireOption(options, "cube");
    const ColorSpaceId spaceId =
        parseColorSpace(requireOption(options, "space"));
    const RGB input = parseRgbTriple(requireOption(options, "rgb"));
    const DomainPolicy policy = policyOption(options);

    const Lut3D lut = readCube(cubePath);
    const Interpolator interpolator(lut, policy);
    const RGB output = renderProbe(interpolator, input);

    std::cout << "status=ok\n"
              << "command=probe\n"
              << "space=" << name(spaceId) << '\n'
              << "domain_policy=" << name(policy) << '\n'
              << "input_r=" << input.r << " input_g=" << input.g
              << " input_b=" << input.b << '\n'
              << "output_r=" << output.r << " output_g=" << output.g
              << " output_b=" << output.b << '\n';
    return kOk;
}

// Bake: rebake a source cube into another space/grid, compare against the
// direct pipeline on a probe grid, and collect patch-image histograms.
int cmdBake(const Options& options) {
    const std::string cubePath = requireOption(options, "cube");
    const ColorSpaceId sourceId =
        parseColorSpace(requireOption(options, "source-space"));
    const ColorSpaceId targetId =
        parseColorSpace(requireOption(options, "target-space"));
    const int targetSize = parsePositiveIntOption(options, "size", 33);
    const int probeGrid = parsePositiveIntOption(options, "probe-grid", 17);
    const double maxError = parseDoubleOption(options, "max-error", 0.0);
    const DomainPolicy policy = policyOption(options);
    const std::string outPath = requireOption(options, "out");
    const std::string patchesPath = requireOption(options, "patches");

    const Lut3D source = readCube(cubePath);
    const ColorSpace sourceSpace(sourceId);
    const ColorSpace targetSpace(targetId);
    const BakeResult result = bakeLut(source, sourceSpace, targetSpace,
                                      targetSize, probeGrid, policy);

    // Patch image gives the required histogram fields; tiles only schedule.
    const Image patches = readPpm(patchesPath);
    const RenderResult rendered =
        renderImage(result.baked, patches, DomainPolicy::Clamp, 16);
    const ImageStats& stats = rendered.stats;

    // Only publish the target cube when the deviation gate passes, so a
    // threshold failure can never leave a seemingly complete output behind.
    const bool exceeded = result.deviation.worst() > maxError;
    if (!exceeded) {
        writeCube(outPath, result.baked);
    }

    std::cout << "status=" << (exceeded ? "threshold_exceeded" : "ok") << '\n'
              << "command=bake\n"
              << "source_space=" << name(sourceId)
              << " target_space=" << name(targetId) << '\n'
              << "grid_size=" << result.baked.size << '\n'
              << "probe_count=" << result.probeCount << '\n'
              << "max_deviation_r=" << result.deviation.maxAbs.r << '\n'
              << "max_deviation_g=" << result.deviation.maxAbs.g << '\n'
              << "max_deviation_b=" << result.deviation.maxAbs.b << '\n'
              << "out_of_domain_pixels=" << stats.outOfDomain << '\n'
              << "clipped_outputs=" << stats.clipped << '\n';
    for (int c = 0; c < 3; ++c) {
        const char channel = c == 0 ? 'r' : c == 1 ? 'g' : 'b';
        std::cout << "hist_before_" << channel << '='
                  << formatHistogram(stats.before[c]) << '\n'
                  << "hist_after_" << channel << '='
                  << formatHistogram(stats.after[c]) << '\n';
    }
    if (exceeded) {
        std::cout << "max_error=" << maxError
                  << " worst_deviation=" << result.deviation.worst() << '\n';
        return kThresholdExceeded;
    }
    std::cout << "out=" << outPath << '\n';
    return kOk;
}

// Render: grade a P6 image through a baked cube, tile rows for scheduling.
int cmdRender(const Options& options) {
    const std::string cubePath = requireOption(options, "cube");
    const ColorSpaceId spaceId =
        parseColorSpace(requireOption(options, "space"));
    const std::string inputPath = requireOption(options, "input");
    const std::string outputPath = requireOption(options, "output");
    const int tileRows = parsePositiveIntOption(options, "tile-rows", 16);
    const DomainPolicy policy = policyOption(options);

    const Lut3D lut = readCube(cubePath);
    const Image input = readPpm(inputPath);
    const RenderResult result = renderImage(lut, input, policy, tileRows);
    writePpm(outputPath, result.graded);

    const ImageStats& stats = result.stats;
    std::cout << "status=ok\n"
              << "command=render\n"
              << "space=" << name(spaceId) << '\n'
              << "width=" << input.width << " height=" << input.height
              << " maxval=" << input.maxval << '\n'
              << "tile_rows=" << tileRows << '\n'
              << "out_of_domain_pixels=" << stats.outOfDomain << '\n'
              << "clipped_outputs=" << stats.clipped << '\n';
    for (int c = 0; c < 3; ++c) {
        const char channel = c == 0 ? 'r' : c == 1 ? 'g' : 'b';
        std::cout << "hist_before_" << channel << '='
                  << formatHistogram(stats.before[c]) << '\n'
                  << "hist_after_" << channel << '='
                  << formatHistogram(stats.after[c]) << '\n';
    }
    std::cout << "output=" << outputPath << '\n';
    return kOk;
}

}  // namespace lut
