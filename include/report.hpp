#pragma once

#include <iosfwd>
#include <string>

#include "media_types.hpp"

namespace pwsync {

// Serializes the report as stable JSON: fixed key order and fixed histogram
// edges so build regressions can diff two runs line by line.
// Returns false (without throwing) if serialization or stream writing fails.
bool writeJsonReport(const SyncReport& report, std::ostream& out);

// Convenience wrapper: writes JSON to a file path ("-" means stdout).
bool writeJsonReportToFile(const SyncReport& report, const std::string& path);

// Human-readable multi-line summary for terminal use.
void writeTextSummary(const SyncReport& report, std::ostream& out);

// Stable JSON string for embedded use (e.g. deterministic diffing in tests).
std::string renderJsonString(const SyncReport& report);

}  // namespace pwsync
