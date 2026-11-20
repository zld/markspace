#include <iostream>

#include "grade/cli.hpp"
#include "grade/error.hpp"

namespace {

// Returns the documented process exit code for every thrown error.
int handleError(const grade::GradeError& error) {
    std::cerr << "lutgrade: " << error.what() << '\n';
    return static_cast<int>(error.code());
}

} // namespace

int main(int argc, char** argv) {
    try {
        grade::Arguments args = grade::parseCommandLine(argc - 1, argv + 1);
        // Command dispatch is wired in later batches; for now prove the
        // parser and exit-code mapping work end to end.
        std::cout << "lutgrade: command '"
                  << (args.command == grade::Command::fixture ? "fixture"
                                                              : "render")
                  << "' accepted\n";
        return static_cast<int>(grade::ExitCode::ok);
    } catch (const grade::GradeError& error) {
        return handleError(error);
    } catch (const std::exception& error) {
        std::cerr << "lutgrade: unexpected error: " << error.what() << '\n';
        return static_cast<int>(grade::ExitCode::render);
    }
}
