#ifndef GRADE_ERROR_HPP
#define GRADE_ERROR_HPP

#include <stdexcept>
#include <string>

namespace grade {

// Process exit codes, part of the terminal contract.
enum class ExitCode : int {
    ok = 0,
    usage = 2,        // invalid command line arguments / options
    format = 3,       // malformed or semantically invalid file content
    io = 4,           // file read/write failure
    render = 5        // rendering failure (bad pixels, worker error, ...)
};

// Base type for every error raised by the tool. The CLI maps subclasses
// onto the documented exit codes.
class GradeError : public std::runtime_error {
public:
    GradeError(const std::string& what, ExitCode code)
        : std::runtime_error(what), code_(code) {}
    ExitCode code() const noexcept { return code_; }

private:
    ExitCode code_;
};

class UsageError : public GradeError {
public:
    explicit UsageError(const std::string& what)
        : GradeError(what, ExitCode::usage) {}
};

class FormatError : public GradeError {
public:
    explicit FormatError(const std::string& what)
        : GradeError(what, ExitCode::format) {}
};

class IoError : public GradeError {
public:
    explicit IoError(const std::string& what)
        : GradeError(what, ExitCode::io) {}
};

class RenderError : public GradeError {
public:
    explicit RenderError(const std::string& what)
        : GradeError(what, ExitCode::render) {}
};

} // namespace grade

#endif
