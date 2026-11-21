#include "lut/model.hpp"

#include <algorithm>
#include <cctype>

namespace lut {
namespace {

std::string lower(std::string_view text) {
    std::string out(text);
    std::transform(out.begin(), out.end(), out.begin(), [](unsigned char c) {
        return static_cast<char>(std::tolower(c));
    });
    return out;
}

}  // namespace

ColorSpaceId parseColorSpace(std::string_view text) {
    const std::string value = lower(text);
    if (value == "srgb" || value == "s-rgb") {
        return ColorSpaceId::Srgb;
    }
    if (value == "display-p3" || value == "p3" || value == "displayp3") {
        return ColorSpaceId::DisplayP3;
    }
    if (value == "rec709" || value == "rec-709" || value == "bt709") {
        return ColorSpaceId::Rec709;
    }
    throw UsageError("unknown colour space: " + std::string(text));
}

DomainPolicy parseDomainPolicy(std::string_view text) {
    const std::string value = lower(text);
    if (value == "clamp") {
        return DomainPolicy::Clamp;
    }
    if (value == "error") {
        return DomainPolicy::Error;
    }
    throw UsageError("domain policy must be 'clamp' or 'error', got: " +
                     std::string(text));
}

}  // namespace lut
