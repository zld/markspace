#include "grade/cube_lut.hpp"

#include <cmath>
#include <fstream>
#include <sstream>

#include "grade/error.hpp"

namespace grade {

namespace {

// Strips leading/trailing ASCII whitespace.
std::string trim(const std::string& in) {
    size_t first = in.find_first_not_of(" \t\r\n");
    if (first == std::string::npos) {
        return "";
    }
    size_t last = in.find_last_not_of(" \t\r\n");
    return in.substr(first, last - first + 1);
}

[[noreturn]] void fail(size_t line, const std::string& message) {
    throw FormatError(".cube line " + std::to_string(line) + ": " + message);
}

// Parses one finite double or reports the offending line.
double parseFinite(const std::string& token, size_t line,
                   const std::string& field) {
    try {
        size_t consumed = 0;
        double value = std::stod(token, &consumed);
        if (consumed != token.size() || !std::isfinite(value)) {
            fail(line, field + " is not a finite number: '" + token + "'");
        }
        return value;
    } catch (const std::invalid_argument&) {
        fail(line, field + " is not a number: '" + token + "'");
    } catch (const std::out_of_range&) {
        fail(line, field + " is out of range: '" + token + "'");
    }
}

} // namespace

// Drives the state machine shared by file and string entry points.
// Declared in the header so CubeLut's friend grant can name it; the
// type is only ever instantiated inside this translation unit.
class CubeParser {
public:
    explicit CubeParser(std::istream& in) : in_(in) {}

    CubeLut run() {
        std::string line;
        while (std::getline(in_, line)) {
            ++lineNo_;
            handleLine(line);
        }
        finalize();
        return lut_;
    }

private:
    void handleLine(const std::string& raw) {
        std::string line = trim(raw);
        if (line.empty() || line[0] == '#') {
            return;
        }
        std::istringstream tokens(line);
        std::string keyword;
        tokens >> keyword;

        if (keyword == "TITLE") {
            parseTitle(tokens);
        } else if (keyword == "LUT_3D_SIZE") {
            parseGrid(tokens);
        } else if (keyword == "LUT_1D_SIZE") {
            fail(lineNo_, "1D LUTs are not supported, only LUT_3D_SIZE");
        } else if (keyword == "DOMAIN_MIN" || keyword == "DOMAIN_MAX") {
            parseDomain(keyword, tokens);
        } else {
            parseDataLine(line);
        }
    }

    void parseTitle(std::istringstream& tokens) {
        if (lut_.title_ != "") {
            fail(lineNo_, "duplicate TITLE directive");
        }
        std::string rest;
        std::getline(tokens, rest);
        rest = trim(rest);
        if (rest.size() >= 2 && rest.front() == '"' && rest.back() == '"') {
            rest = rest.substr(1, rest.size() - 2);
        }
        if (rest.empty()) {
            fail(lineNo_, "TITLE must not be empty");
        }
        lut_.title_ = rest;
    }

    void parseGrid(std::istringstream& tokens) {
        if (lut_.gridSize_ != 0) {
            fail(lineNo_, "duplicate LUT_3D_SIZE directive");
        }
        std::string token;
        if (!(tokens >> token)) {
            fail(lineNo_, "LUT_3D_SIZE requires an integer");
        }
        try {
            size_t consumed = 0;
            int size = std::stoi(token, &consumed, 10);
            if (consumed != token.size() || size < kMinCubeGrid ||
                size > kMaxCubeGrid) {
                fail(lineNo_, "LUT_3D_SIZE must be an integer in [" +
                                  std::to_string(kMinCubeGrid) + ", " +
                                  std::to_string(kMaxCubeGrid) + "], got '" +
                                  token + "'");
            }
            lut_.gridSize_ = size;
        } catch (const std::invalid_argument&) {
            fail(lineNo_, "LUT_3D_SIZE must be an integer, got '" + token +
                              "'");
        } catch (const std::out_of_range&) {
            fail(lineNo_, "LUT_3D_SIZE is out of range, got '" + token + "'");
        }
        if (tokens >> token) {
            fail(lineNo_, "LUT_3D_SIZE takes exactly one value");
        }
    }

    void parseDomain(const std::string& keyword, std::istringstream& tokens) {
        bool isMin = keyword == "DOMAIN_MIN";
        auto& target = isMin ? lut_.domainMin_ : lut_.domainMax_;
        bool& seen = isMin ? sawDomainMin_ : sawDomainMax_;
        if (seen) {
            fail(lineNo_, "duplicate " + keyword + " directive");
        }
        for (size_t channel = 0; channel < 3; ++channel) {
            std::string token;
            if (!(tokens >> token)) {
                fail(lineNo_, keyword + " requires three values");
            }
            target[channel] = parseFinite(token, lineNo_, keyword);
        }
        std::string extra;
        if (tokens >> extra) {
            fail(lineNo_, keyword + " takes exactly three values");
        }
        seen = true;
    }

    void parseDataLine(const std::string& line) {
        if (lut_.gridSize_ == 0) {
            fail(lineNo_,
                 "data row appears before LUT_3D_SIZE was declared");
        }
        std::istringstream tokens(line);
        CubeLut::Rgb entry{};
        for (size_t channel = 0; channel < 3; ++channel) {
            std::string token;
            if (!(tokens >> token)) {
                fail(lineNo_, "data row requires exactly three values");
            }
            entry[channel] = parseFinite(token, lineNo_, "data value");
        }
        std::string extra;
        if (tokens >> extra) {
            fail(lineNo_, "data row requires exactly three values");
        }
        lut_.data_.push_back(entry);
    }

    void finalize() {
        if (lut_.gridSize_ == 0) {
            fail(lineNo_ + 1, "missing LUT_3D_SIZE directive");
        }
        for (size_t channel = 0; channel < 3; ++channel) {
            if (!(lut_.domainMin_[channel] < lut_.domainMax_[channel])) {
                fail(lineNo_ + 1,
                     "DOMAIN_MIN must be strictly below DOMAIN_MAX for every "
                     "channel");
            }
        }
        size_t expected = static_cast<size_t>(lut_.gridSize_) *
                          static_cast<size_t>(lut_.gridSize_) *
                          static_cast<size_t>(lut_.gridSize_);
        if (lut_.data_.size() != expected) {
            fail(lineNo_ + 1, "expected " + std::to_string(expected) +
                                  " data entries, found " +
                                  std::to_string(lut_.data_.size()));
        }
    }

    std::istream& in_;
    size_t lineNo_ = 0;
    bool sawDomainMin_ = false;
    bool sawDomainMax_ = false;
    CubeLut lut_;
};

size_t CubeLut::index(int r, int g, int b) const noexcept {
    size_t n = static_cast<size_t>(gridSize_);
    return static_cast<size_t>(r) + static_cast<size_t>(g) * n +
           static_cast<size_t>(b) * n * n;
}

CubeLut::Rgb CubeLut::normalizeToDomain(const Rgb& value) const {
    Rgb normalized{};
    for (size_t channel = 0; channel < 3; ++channel) {
        double span = domainMax_[channel] - domainMin_[channel];
        normalized[channel] = (value[channel] - domainMin_[channel]) / span;
    }
    return normalized;
}

CubeLut parseCubeString(const std::string& content) {
    std::istringstream stream(content);
    return CubeParser(stream).run();
}

CubeLut parseCubeFile(const std::string& path) {
    std::ifstream in(path);
    if (!in) {
        throw IoError("cannot open LUT file: " + path);
    }
    return CubeParser(in).run();
}

} // namespace grade
