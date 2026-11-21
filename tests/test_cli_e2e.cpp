#include <cstdio>
#include <cstdlib>
#include <filesystem>
#include <string>
#include <sys/wait.h>

#include "test_helpers.hpp"

namespace {

std::string lutctlPath() {
    const char* fromEnv = std::getenv("LUTCTL_BIN");
    return fromEnv != nullptr ? fromEnv : "lutctl";
}

// Runs a command, returns the raw shell exit status.
int runShell(const std::string& command) {
    const int status = std::system(command.c_str());
    return status;
}

bool succeeded(const std::string& command) {
    const int status = runShell(command);
#ifdef _WIN32
    return status == 0;
#else
    return WIFEXITED(status) && WEXITSTATUS(status) == 0;
#endif
}

int exitCode(const std::string& command) {
    const int status = runShell(command);
#ifdef _WIN32
    return status;
#else
    return WIFEXITED(status) ? WEXITSTATUS(status) : -1;
#endif
}

}  // namespace

int main() {
    const std::string bin = lutctlPath();
    const std::string dir = luttest::scratchDir();
    const std::string demo = dir + "/demo";
    const std::string cube = demo + "/source.cube";
    const std::string patches = demo + "/patches.ppm";
    const std::string baked = dir + "/baked.cube";
    const std::string graded = dir + "/graded.ppm";

    // fixture-gen succeeds and publishes both artifacts.
    CHECK(succeeded(bin + " fixture-gen --out " + demo +
                    " --patch-space display-p3"));
    CHECK(std::filesystem::exists(cube));
    CHECK(std::filesystem::exists(patches));

    // Happy-path bake: exit 0, target cube published.
    const std::string bake =
        bin + " bake --cube " + cube +
        " --source-space srgb --target-space display-p3 --size 33 "
        "--probe-grid 17 --patches " + patches + " --out " + baked +
        " --max-error 0.02";
    CHECK(exitCode(bake) == 0);
    CHECK(std::filesystem::exists(baked));

    // Probe and render both succeed; render produces a P6 output.
    CHECK(succeeded(bin + " probe --cube " + baked +
                    " --space display-p3 --rgb 0.2,0.4,0.6"));
    CHECK(succeeded(bin + " render --cube " + baked +
                    " --space display-p3 --input " + patches + " --output " +
                    graded + " --tile-rows 16"));
    CHECK(std::filesystem::exists(graded));

    // Exit 2: usage errors.
    CHECK(exitCode(bin + " bogus-command") == 2);
    CHECK(exitCode(bin + " probe --cube " + baked +
                   " --space display-p3 --rgb not-a-triple") == 2);

    // Exit 3: malformed LUT.
    const std::string badCube = dir + "/bad.cube";
    {
        std::FILE* f = std::fopen(badCube.c_str(), "w");
        std::fputs("LUT_3D_SIZE 2\n0.5 0.5 0.5\n", f);
        std::fclose(f);
    }
    CHECK(exitCode(bin + " probe --cube " + badCube +
                   " --space srgb --rgb 0,0,0") == 3);

    // Exit 4: missing input file.
    CHECK(exitCode(bin + " probe --cube " + dir +
                   "/missing.cube --space srgb --rgb 0,0,0") == 4);

    // Exit 5: deviation gate fails and no target file is left behind.
    const std::string rejected = dir + "/rejected.cube";
    const int thresholdStatus = exitCode(
        bin + " bake --cube " + cube +
        " --source-space srgb --target-space display-p3 --size 2 "
        "--probe-grid 17 --patches " + patches + " --out " + rejected +
        " --max-error 0.0001");
    CHECK(thresholdStatus == 5);
    CHECK(!std::filesystem::exists(rejected));

    return luttest::finish();
}
