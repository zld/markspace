#pragma once
#include <cstdint>
#include <optional>
#include <string>
#include <vector>

#include "ass_model.hpp"

namespace assaudit {

std::string trimCopy(std::string s);
std::string toLower(std::string s);
bool iequals(const std::string& a, const std::string& b);

// Split on commas; when maxParts > 0 the final field keeps every remaining
// comma (ASS Text / last-column semantics).
std::vector<std::string> splitFields(const std::string& s, int maxParts = 0);

std::optional<long long> parseTimecode(const std::string& s);
bool parseDouble(const std::string& s, double& out);
bool parseInt(const std::string& s, int& out);
std::optional<AssColor> parseAssColor(const std::string& s);
std::string colorToAss(int a, int r, int g, int b);

uint64_t fnv1a64(const uint8_t* data, size_t len, uint64_t seed = 1469598103934665603ULL);
std::string jsonEscape(const std::string& s);
std::string hex64(uint64_t v);

}  // namespace assaudit
