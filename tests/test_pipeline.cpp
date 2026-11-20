#include <cstdint>
#include <string>
#include <vector>

#include "grade/cli.hpp"
#include "grade/color.hpp"
#include "grade/cube_lut.hpp"
#include "grade/error.hpp"

#include "framework.hpp"

using namespace grade;

namespace {

// Builds a valid identity .cube of the requested grid size.
std::string identityCubeText(int grid) {
    std::string text = "TITLE \"identity\"\nLUT_3D_SIZE " +
                       std::to_string(grid) + "\n";
    for (int b = 0; b < grid; ++b) {
        for (int g = 0; g < grid; ++g) {
            for (int r = 0; r < grid; ++r) {
                double rv = static_cast<double>(r) / (grid - 1);
                double gv = static_cast<double>(g) / (grid - 1);
                double bv = static_cast<double>(b) / (grid - 1);
                text += std::to_string(rv) + " " + std::to_string(gv) +
                        " " + std::to_string(bv) + "\n";
            }
        }
    }
    return text;
}

} // namespace

// --- CLI ------------------------------------------------------------------

GRADE_TEST(cli_rejects_unknown_command) {
    const char* argv[] = {"explode"};
    GRADE_REQUIRE_THROWS(parseCommandLine(1, const_cast<char**>(argv)),
                         UsageError);
}

GRADE_TEST(cli_parses_full_render_options) {
    const char* argv[] = {"render",  "--input", "in.ppm",
                          "--lut",   "look.cube", "--output", "out.ppm",
                          "--input-space", "display-p3",
                          "--lut-space", "srgb",
                          "--output-space", "display-p3",
                          "--interpolation", "tetrahedral",
                          "--path", "compare", "--threads", "8"};
    Arguments args = parseCommandLine(19, const_cast<char**>(argv));
    GRADE_REQUIRE(args.command == Command::render);
    GRADE_REQUIRE(args.render.input == "in.ppm");
    GRADE_REQUIRE(args.render.lut == "look.cube");
    GRADE_REQUIRE(args.render.output == "out.ppm");
    GRADE_REQUIRE(args.render.inputSpace == ColorSpaceId::displayP3);
    GRADE_REQUIRE(args.render.lutSpace == ColorSpaceId::srgb);
    GRADE_REQUIRE(args.render.outputSpace == ColorSpaceId::displayP3);
    GRADE_REQUIRE(args.render.interpolation == InterpId::tetrahedral);
    GRADE_REQUIRE(args.render.path == PathMode::compare);
    GRADE_REQUIRE(args.render.threads == 8);
}

GRADE_TEST(cli_requires_render_inputs) {
    const char* argv[] = {"render", "--input", "in.ppm"};
    GRADE_REQUIRE_THROWS(parseCommandLine(3, const_cast<char**>(argv)),
                         UsageError);
}

GRADE_TEST(cli_rejects_thread_and_grid_bounds) {
    const char* badThreads[] = {"render", "--input", "a", "--lut", "b",
                                "--output", "c", "--threads", "0"};
    GRADE_REQUIRE_THROWS(
        parseCommandLine(9, const_cast<char**>(badThreads)), UsageError);
    const char* badGrid[] = {"fixture", "--grid", "66"};
    GRADE_REQUIRE_THROWS(
        parseCommandLine(3, const_cast<char**>(badGrid)), UsageError);
}

GRADE_TEST(cli_rejects_path_traversal) {
    GRADE_REQUIRE_THROWS(validatePath("../secret.ppm"), UsageError);
    GRADE_REQUIRE_THROWS(validatePath("dir/../../x"), UsageError);
    validatePath("build/demo/input.ppm"); // must not throw
}

GRADE_TEST(cli_rejects_bad_enum_values) {
    const char* argv[] = {"render",  "--input", "a", "--lut", "b",
                          "--output", "c", "--interpolation", "nearest"};
    GRADE_REQUIRE_THROWS(parseCommandLine(8, const_cast<char**>(argv)),
                         UsageError);
}

// --- color ----------------------------------------------------------------

GRADE_TEST(color_srgb_transfer_round_trips) {
    for (int i = 0; i <= 50; ++i) {
        double encoded = static_cast<double>(i) / 50.0;
        double linear = decodeTransfer(ColorSpaceId::srgb, encoded);
        double back = encodeTransfer(ColorSpaceId::srgb, linear);
        GRADE_REQUIRE_NEAR(back, encoded, 1.0e-12);
    }
}

GRADE_TEST(color_decode_matches_known_srgb_value) {
    GRADE_REQUIRE_NEAR(decodeTransfer(ColorSpaceId::srgb, 0.5), 0.21404114,
                       1.0e-7);
    GRADE_REQUIRE_NEAR(decodeTransfer(ColorSpaceId::displayP3, 0.5),
                       0.21404114, 1.0e-7);
}

GRADE_TEST(color_identity_conversion_is_exact) {
    Triple red{{1.0, 0.0, 0.0}};
    Triple same = convertEncoded(ColorSpaceId::srgb,
                                 ColorSpaceId::srgb, red);
    GRADE_REQUIRE_NEAR(same[0], 1.0, 1.0e-15);
    GRADE_REQUIRE_NEAR(same[1], 0.0, 1.0e-15);
}

GRADE_TEST(color_cross_space_round_trip) {
    Triple color{{0.9, 0.3, 0.6}};
    Triple p3 = convertEncoded(ColorSpaceId::srgb, ColorSpaceId::displayP3,
                               color);
    Triple back = convertEncoded(ColorSpaceId::displayP3,
                                 ColorSpaceId::srgb, p3);
    for (size_t channel = 0; channel < 3; ++channel) {
        GRADE_REQUIRE_NEAR(back[channel], color[channel], 1.0e-12);
    }
}

GRADE_TEST(color_white_point_is_d65) {
    Triple white = multiply(rgbToXyzMatrix(ColorSpaceId::srgb),
                            Triple{{1.0, 1.0, 1.0}});
    GRADE_REQUIRE_NEAR(white[0], 0.950456, 1.0e-5);
    GRADE_REQUIRE_NEAR(white[1], 1.0, 1.0e-12);
    GRADE_REQUIRE_NEAR(white[2], 1.089058, 1.0e-5);
}

GRADE_TEST(color_singular_matrix_is_rejected) {
    Mat3 zero{{{{0.0, 0.0, 0.0}},
               {{0.0, 0.0, 0.0}},
               {{0.0, 0.0, 0.0}}}};
    GRADE_REQUIRE_THROWS(invertMatrix3(zero), FormatError);
}

GRADE_TEST(color_matrix_inversion_round_trips) {
    const Mat3& forward = rgbToXyzMatrix(ColorSpaceId::displayP3);
    Mat3 inverse = invertMatrix3(forward);
    Triple vector{{0.3, 0.7, 0.42}};
    Triple restored = multiply(inverse, multiply(forward, vector));
    for (size_t channel = 0; channel < 3; ++channel) {
        GRADE_REQUIRE_NEAR(restored[channel], vector[channel], 1.0e-12);
    }
}

// --- cube LUT -------------------------------------------------------------

GRADE_TEST(cube_parses_valid_file_with_ordering) {
    CubeLut lut = parseCubeString(identityCubeText(3));
    GRADE_REQUIRE(lut.gridSize() == 3);
    GRADE_REQUIRE(lut.title() == "identity");
    GRADE_REQUIRE(lut.data().size() == 27u);
    // Red varies fastest: (r=1,g=0,b=0) is the second entry.
    GRADE_REQUIRE_NEAR(lut.data()[1][0], 0.5, 1.0e-12);
    GRADE_REQUIRE(lut.index(1, 1, 0) == 4u);
}

GRADE_TEST(cube_rejects_duplicate_directives) {
    GRADE_REQUIRE_THROWS(parseCubeString("LUT_3D_SIZE 2\nLUT_3D_SIZE 2\n"),
                         FormatError);
    GRADE_REQUIRE_THROWS(
        parseCubeString("LUT_3D_SIZE 2\nDOMAIN_MIN 0 0 0\nDOMAIN_MIN 0 0 0\n"),
        FormatError);
}

GRADE_TEST(cube_rejects_bad_grid_sizes) {
    GRADE_REQUIRE_THROWS(parseCubeString("LUT_3D_SIZE 1\n"), FormatError);
    GRADE_REQUIRE_THROWS(parseCubeString("LUT_3D_SIZE 66\n"), FormatError);
}

GRADE_TEST(cube_rejects_wrong_entry_count_with_line_number) {
    try {
        parseCubeString("LUT_3D_SIZE 2\n1 2 3\n");
        GRADE_REQUIRE(false);
    } catch (const FormatError& error) {
        std::string message = error.what();
        GRADE_REQUIRE(message.find("line") != std::string::npos);
        GRADE_REQUIRE(message.find("expected 8") != std::string::npos);
    }
}

GRADE_TEST(cube_rejects_non_finite_and_malformed_values) {
    GRADE_REQUIRE_THROWS(parseCubeString("LUT_3D_SIZE 2\n1 2 nan\n"),
                         FormatError);
    GRADE_REQUIRE_THROWS(parseCubeString("LUT_3D_SIZE 2\n1 2 inf\n"),
                         FormatError);
    GRADE_REQUIRE_THROWS(parseCubeString("LUT_3D_SIZE 2\n1 2\n"),
                         FormatError);
}

GRADE_TEST(cube_rejects_invalid_domain) {
    GRADE_REQUIRE_THROWS(
        parseCubeString("LUT_3D_SIZE 2\nDOMAIN_MIN 1 1 1\n"
                        "DOMAIN_MAX 1 1 1\n"),
        FormatError);
}

GRADE_TEST(cube_normalizes_custom_domain) {
    std::string text = "LUT_3D_SIZE 2\nDOMAIN_MIN 0.25 0.25 0.25\n"
                       "DOMAIN_MAX 0.75 0.75 0.75\n";
    for (int entry = 0; entry < 8; ++entry) {
        text += "0 0 0\n";
    }
    CubeLut lut = parseCubeString(text);
    CubeLut::Rgb normalized = lut.normalizeToDomain({{0.5, 0.25, 0.75}});
    GRADE_REQUIRE_NEAR(normalized[0], 0.5, 1.0e-15);
    GRADE_REQUIRE_NEAR(normalized[1], 0.0, 1.0e-15);
    GRADE_REQUIRE_NEAR(normalized[2], 1.0, 1.0e-15);
}

GRADE_TEST(cube_supports_extreme_grid_sizes) {
    GRADE_REQUIRE(parseCubeString(identityCubeText(2)).gridSize() == 2);
    GRADE_REQUIRE(parseCubeString(identityCubeText(65)).gridSize() == 65);
}

GRADE_TEST(cube_rejects_1d_lut) {
    GRADE_REQUIRE_THROWS(parseCubeString("LUT_1D_SIZE 4\n"), FormatError);
}

int main() { return gradetest::runAll(); }
