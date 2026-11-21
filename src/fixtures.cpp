#include "lut/fixtures.hpp"

#include <filesystem>

#include "lut/cube_reader.hpp"

namespace lut {
namespace {

double smoothChannel(double r, double g, double b, double lift,
                     double mainWeight, double cross1, double cross2) {
    return lift + mainWeight * r + cross1 * g + cross2 * b;
}

}  // namespace

Lut3D makeFixtureLut(int size) {
    if (size < 2 || size > 65) {
        throw UsageError("fixture LUT size out of range [2,65]");
    }
    Lut3D lut;
    lut.size = size;
    lut.nodes.resize(lut.nodeCount());
    for (int b = 0; b < size; ++b) {
        for (int g = 0; g < size; ++g) {
            for (int r = 0; r < size; ++r) {
                const double x = static_cast<double>(r) / (size - 1);
                const double y = static_cast<double>(g) / (size - 1);
                const double z = static_cast<double>(b) / (size - 1);
                // Lift + main axis + cross mixing; coefficients keep the
                // result inside [0.03, 0.97] for every input combination.
                RGB node;
                node.r = smoothChannel(x, y, z, 0.05, 0.75, 0.10, 0.07);
                node.g = smoothChannel(y, z, x, 0.04, 0.70, 0.12, 0.08);
                node.b = smoothChannel(z, x, y, 0.03, 0.80, 0.06, 0.08);
                lut.at(r, g, b) = node;
            }
        }
    }
    return lut;
}

Image makeFixtureImage(std::uint32_t maxval) {
    if (maxval == 0 || maxval > 65535) {
        throw UsageError("fixture maxval out of PPM range");
    }
    Image image;
    image.width = kFixtureImageSide;
    image.height = kFixtureImageSide;
    image.maxval = maxval;
    image.pixels.resize(image.pixelCount());

    std::uint32_t i = 0;
    for (std::uint32_t y = 0; y < image.height; ++y) {
        for (std::uint32_t x = 0; x < image.width; ++x) {
            // Deterministic modular ramps give broad channel coverage while
            // every value remains a legal encoded sample in [0,1].
            const double r = static_cast<double>((x * 7 + y * 3) % 64) / 63.0;
            const double g = static_cast<double>((x * 5 + y * 11) % 64) / 63.0;
            const double b = static_cast<double>((x * 13 + y * 2) % 64) / 63.0;
            image.pixels[i++] = RGB{r, g, b};
        }
    }
    return image;
}

void writeFixtures(const std::string& outDir, ColorSpaceId /*patchSpace*/) {
    std::error_code ec;
    std::filesystem::create_directories(outDir, ec);
    if (ec) {
        throw IoError("cannot create fixture directory " + outDir + ": " +
                      ec.message());
    }
    const Lut3D lut = makeFixtureLut();
    const Image patches = makeFixtureImage();
    writeCube((std::filesystem::path(outDir) / "source.cube").string(), lut);
    writePpm((std::filesystem::path(outDir) / "patches.ppm").string(),
             patches);
}

}  // namespace lut
