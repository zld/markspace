// main.cpp — CLI entry point (skeleton; modules are added incrementally).
//
// Exit code contract:
//   0  analysis completed, no overflow
//   2  input or configuration error
//   3  font or output error
//   4  analysis completed but at least one subtitle overflows the safe area
#include <cstdio>
#include <string>
#include <vector>

namespace {

constexpr int EXIT_OK = 0;
constexpr int EXIT_INPUT_ERROR = 2;
constexpr int EXIT_FONT_OUTPUT_ERROR = 3;
constexpr int EXIT_OVERFLOW = 4;

void print_usage(FILE* out) {
    std::fputs(
        "usage: subtitle-metrics --font FONT --input INPUT --width PX\n"
        "                        [--safe-left PX] [--safe-right PX]\n"
        "                        [--safe-top PX] [--safe-bottom PX]\n"
        "                        [--font-size PX] [--scale N]\n"
        "                        [--height PX] [--wrap space|punctuation|hard]\n"
        "                        [--json-out PATH]\n"
        "       subtitle-metrics --make-fixture PATH\n",
        out);
}

}  // namespace

int main(int argc, char** argv) {
    std::vector<std::string> args(argv + 1, argv + argc);
    if (args.empty()) {
        print_usage(stderr);
        return EXIT_INPUT_ERROR;
    }
    for (const auto& a : args) {
        if (a == "--help" || a == "-h") {
            print_usage(stdout);
            return EXIT_OK;
        }
    }
    std::fputs("subtitle-metrics: skeleton build, analysis not yet wired\n",
               stderr);
    (void)EXIT_FONT_OUTPUT_ERROR;
    (void)EXIT_OVERFLOW;
    return EXIT_INPUT_ERROR;
}
