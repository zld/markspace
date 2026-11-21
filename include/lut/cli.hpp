#ifndef LUT_CLI_HPP
#define LUT_CLI_HPP

namespace lut {

// Process exit codes shared by all commands.
enum ExitCode : int {
    kOk = 0,
    kUsage = 2,
    kInvalidLut = 3,
    kIoFailure = 4,
    kThresholdExceeded = 5,
};

// Runs one lutctl invocation. argv excludes the program name. Diagnostic
// fields are printed on both success and failure paths; the returned value is
// the process exit code.
int runCommand(int argc, char** argv);

}  // namespace lut

#endif  // LUT_CLI_HPP
