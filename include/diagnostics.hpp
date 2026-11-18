// Small helpers for building diagnostics.
#pragma once

#include <string>
#include <utility>

#include "byte_source.hpp"

namespace h264inspect {

inline void add_diagnostic(std::vector<Diagnostic>& errors, ErrorKind kind,
                           std::uint64_t offset, std::string message) {
    Diagnostic diagnostic;
    diagnostic.kind = kind;
    diagnostic.offset = offset;
    diagnostic.message = std::move(message);
    errors.push_back(std::move(diagnostic));
}

}  // namespace h264inspect
