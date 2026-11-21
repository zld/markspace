#include "lut/cli_internal.hpp"

#include <array>
#include <sstream>

namespace lut {

std::string requireOption(const Options& options, const std::string& key) {
    const auto it = options.find(key);
    if (it == options.end() || it->second.empty()) {
        throw UsageError("missing required option --" + key);
    }
    return it->second;
}

int parsePositiveIntOption(const Options& options, const std::string& key,
                           int fallback) {
    const auto it = options.find(key);
    if (it == options.end()) {
        return fallback;
    }
    std::size_t consumed = 0;
    int value = 0;
    try {
        value = std::stoi(it->second, &consumed);
    } catch (const std::exception&) {
        throw UsageError("invalid integer for --" + key + ": " + it->second);
    }
    if (consumed != it->second.size() || value <= 0) {
        throw UsageError("--" + key + " must be positive, got: " + it->second);
    }
    return value;
}

double parseDoubleOption(const Options& options, const std::string& key,
                         double fallback) {
    const auto it = options.find(key);
    if (it == options.end()) {
        return fallback;
    }
    std::size_t consumed = 0;
    double value = 0.0;
    try {
        value = std::stod(it->second, &consumed);
    } catch (const std::exception&) {
        throw UsageError("invalid number for --" + key + ": " + it->second);
    }
    if (consumed != it->second.size() || !std::isfinite(value) ||
        value < 0.0) {
        throw UsageError("--" + key + " must be non-negative: " + it->second);
    }
    return value;
}

RGB parseRgbTriple(const std::string& text) {
    std::array<double, 3> values{};
    std::size_t filled = 0;
    std::stringstream ss(text);
    std::string token;
    while (std::getline(ss, token, ',')) {
        if (filled >= 3) {
            throw UsageError("rgb expects exactly R,G,B values: " + text);
        }
        try {
            std::size_t consumed = 0;
            const double value = std::stod(token, &consumed);
            if (consumed != token.size() || !std::isfinite(value)) {
                throw UsageError("invalid rgb component: " + token);
            }
            values[filled++] = value;
        } catch (const std::invalid_argument&) {
            throw UsageError("invalid rgb component: " + token);
        }
    }
    if (filled != 3) {
        throw UsageError("rgb expects exactly R,G,B values: " + text);
    }
    return RGB{values[0], values[1], values[2]};
}

DomainPolicy policyOption(const Options& options) {
    const auto it = options.find("domain-policy");
    if (it == options.end()) {
        return DomainPolicy::Clamp;
    }
    return parseDomainPolicy(it->second);
}

std::string formatHistogram(const Histogram& hist) {
    std::string out = "[";
    for (std::size_t i = 0; i < hist.size(); ++i) {
        if (i != 0) {
            out += ',';
        }
        out += std::to_string(hist[i]);
    }
    out += ']';
    return out;
}

}  // namespace lut
