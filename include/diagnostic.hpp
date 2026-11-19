#pragma once

#include <cstddef>
#include <cstdint>
#include <string>
#include <utility>
#include <vector>

namespace h264 {

// Severity strings used throughout the tool.
inline constexpr const char* kSeverityError = "error";
inline constexpr const char* kSeverityWarning = "warning";

struct Diagnostic {
    std::uint64_t offset = 0;   // byte offset in the original input file
    std::string severity;       // kSeverityError or kSeverityWarning
    std::string code;           // stable machine-readable identifier
    std::string message;        // human-readable description
};

// Accumulates diagnostics with a hard cap so a stream with a problem in every
// byte cannot exhaust memory.  Over-cap diagnostics are counted, not stored.
class DiagnosticSink {
public:
    static constexpr std::size_t kMaxStored = 4096;

    void add(std::uint64_t offset, const char* severity, const char* code,
             std::string message);

    void error(std::uint64_t offset, const char* code, std::string message) {
        add(offset, kSeverityError, code, std::move(message));
    }
    void warning(std::uint64_t offset, const char* code, std::string message) {
        add(offset, kSeverityWarning, code, std::move(message));
    }

    bool has_error() const { return error_count_ != 0; }
    bool has_warning() const { return warning_count_ != 0; }

    const std::vector<Diagnostic>& items() const { return items_; }
    std::size_t dropped() const { return dropped_; }
    std::size_t error_count() const { return error_count_; }
    std::size_t warning_count() const { return warning_count_; }

private:
    std::vector<Diagnostic> items_;
    std::size_t dropped_ = 0;
    std::size_t error_count_ = 0;
    std::size_t warning_count_ = 0;
};

}  // namespace h264
