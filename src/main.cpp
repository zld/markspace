#include <exception>
#include <iostream>
#include <vector>

#include "cli.hpp"
#include "media_types.hpp"

int main(int argc, char** argv) {
    using namespace pwsync;
    CliOptions options;
    try {
        options = parseCli(argc, argv);
    } catch (const std::exception& e) {
        std::cerr << "argument error: " << e.what() << "\n\n";
        printUsage(std::cerr, argv[0]);
        return static_cast<int>(ExitCode::Usage);
    }

    std::vector<MediaError> errors;
    ExitCode code = ExitCode::Ok;
    try {
        code = runPipeline(options, errors);
    } catch (const std::exception& e) {
        std::cerr << "fatal: " << e.what() << "\n";
        return static_cast<int>(ExitCode::MediaError);
    }
    for (const auto& e : errors) {
        std::cerr << "[" << toString(e.category) << " stream=" << e.stream_index
                  << "] " << e.message << "\n";
    }
    return static_cast<int>(code);
}
