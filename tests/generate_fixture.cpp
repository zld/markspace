#include "bit_writer.hpp"

#include <cstdio>
#include <cstring>
#include <fstream>
#include <string>

namespace {

int usage() {
    std::fprintf(stderr, "usage: generate_fixture --output <file.264>\n");
    return 64;
}

}  // namespace

int main(int argc, char** argv) {
    std::string output;
    for (int i = 1; i < argc; ++i) {
        if (std::strcmp(argv[i], "--output") == 0 && i + 1 < argc) {
            output = argv[++i];
        } else {
            return usage();
        }
    }
    if (output.empty()) return usage();

    const auto stream = h264::gen::build_demo_stream();
    std::ofstream file(output, std::ios::binary | std::ios::trunc);
    if (!file) {
        std::fprintf(stderr, "cannot open output file: %s\n", output.c_str());
        return 66;
    }
    file.write(reinterpret_cast<const char*>(stream.data()),
               static_cast<std::streamsize>(stream.size()));
    if (!file) {
        std::fprintf(stderr, "write failed: %s\n", output.c_str());
        return 66;
    }
    std::printf("wrote %zu deterministic fixture bytes to %s\n",
                stream.size(), output.c_str());
    return 0;
}
