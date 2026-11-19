#include <algorithm>
#include <cstdint>
#include <exception>
#include <fstream>
#include <iostream>
#include <sstream>
#include <string>
#include <vector>

#include "color/cube_parser.hpp"
#include "color/demo.hpp"
#include "color/histogram.hpp"
#include "color/render_paths.hpp"
#include "color/report.hpp"

namespace {

using namespace color;

constexpr int kExitSuccess = 0;
constexpr int kExitArgs = 2;
constexpr int kExitData = 3;
constexpr int kExitProcessing = 4;

std::string readFile(const std::string& path) {
    std::ifstream input(path, std::ios::binary);
    if (!input) {
        throw LutError("cannot open LUT file: " + path);
    }
    std::ostringstream buffer;
    buffer << input.rdbuf();
    if (input.bad()) {
        throw LutError("cannot read LUT file: " + path);
    }
    return buffer.str();
}

LUT loadLut(const Options& options) {
    if (!options.lutFile.empty()) {
        return parseCubeText(readFile(options.lutFile), options.lutFile);
    }
    // The embedded demo LUT takes the same path as an external file: serialise
    // then parse, so the demo exercises the parser end to end.
    const LUT generated = makeDemoLut(options.size);
    const std::string text = cubeTextWithOrder(generated, options.lutOrder);
    LUT parsed = parseCubeText(text, "<generated>");
    validateLut(parsed);
    return parsed;
}

// Converts in place: at the 16M pixel cap a second full buffer would cost
// another ~400 MB for nothing.
void convertInPlace(std::vector<RGB>& pixels, ColorSpace from, ColorSpace to) {
    if (from == to) return;
    for (std::size_t i = 0; i < pixels.size(); ++i) {
        pixels[i] = convertColor(from, to, pixels[i], i);
    }
}

void writeOutput(const std::string& path, const std::string& payload) {
    std::ofstream output(path, std::ios::binary | std::ios::trunc);
    if (!output) {
        throw ProcessingError("cannot open output file for writing: " + path);
    }
    output << payload;
    output.flush();
    if (!output) {
        throw ProcessingError("failed to write output file: " + path);
    }
}

int run(const Options& options) {
    const LUT lut = loadLut(options);
    std::vector<RGB> working =
        makeDemoPixels(options.pixels, options.inputSpace, options.seed);
    convertInPlace(working, options.inputSpace, options.lutSpace);

    // The reference path always uses one thread; the parallel path uses the
    // requested worker count. Both call the same interpolation kernel.
    const std::vector<RGB> baseline = renderScalar(lut, working, options.boundary);
    const std::vector<RGB> threaded =
        renderParallel(lut, working, options.boundary, options.threads);

    Report report;
    report.inputSpace = colorSpaceName(options.inputSpace);
    report.lutSpace = colorSpaceName(options.lutSpace);
    report.boundary = options.boundary == BoundaryPolicy::Clamp ? "clamp" : "reject";
    report.lutSize = lut.nx;
    report.lutDims[0] = lut.nx;
    report.lutDims[1] = lut.ny;
    report.lutDims[2] = lut.nz;
    report.pixels = working.size();
    report.threads = options.threads;
    report.seed = options.seed;
    report.maxAbsDiff = maxAbsDiff(baseline, threaded);
    report.before = buildHistograms(working);
    report.after = buildHistograms(threaded);

    const std::string countProblem = validateCounts(report);
    if (!countProblem.empty()) {
        throw ProcessingError("inconsistent report: " + countProblem);
    }

    const std::string payload = options.format == OutputFormat::Json ? renderJson(report)
                                                                     : renderText(report);
    // Write the file before stdout so an output failure never leaves a
    // complete-looking summary on the terminal.
    if (!options.outputPath.empty()) writeOutput(options.outputPath, payload);
    std::cout << payload;
    std::cout.flush();
    if (!std::cout) {
        throw ProcessingError("failed to write report to standard output");
    }
    return kExitSuccess;
}

}  // namespace

int main(int argc, char** argv) {
    try {
        const color::Options options = color::parseOptions(argc, argv);
        if (options.requestHelp) {
            std::cout << color::usage();
            return kExitSuccess;
        }
        return run(options);
    } catch (const std::invalid_argument& error) {
        std::cerr << "error: " << error.what() << "\n\n" << color::usage();
        return kExitArgs;
    } catch (const color::ProcessingError& error) {
        std::cerr << "error: " << error.what() << "\n";
        return kExitProcessing;
    } catch (const color::LutError& error) {
        std::cerr << "error: " << error.what() << "\n";
        return kExitData;
    } catch (const std::bad_alloc&) {
        std::cerr << "error: out of memory\n";
        return kExitProcessing;
    } catch (const std::exception& error) {
        std::cerr << "error: " << error.what() << "\n";
        return kExitProcessing;
    }
}
