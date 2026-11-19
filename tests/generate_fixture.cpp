#include <cstdio>
#include <string>

#include "fixture_generator.hpp"

// Standalone fixture generator used during development and by ctest.
int main(int argc, char** argv) {
    if (argc != 2) {
        std::fprintf(stderr, "usage: %s OUTPUT.mp4\n", argv[0]);
        return 2;
    }
    pwsync::FixtureOptions options;
    options.output_path = argv[1];
    std::string error;
    if (!pwsync::generateFixture(options, error)) {
        std::fprintf(stderr, "fixture generation failed: %s\n", error.c_str());
        return 3;
    }
    std::printf("fixture written: %s\n", argv[1]);
    return 0;
}
