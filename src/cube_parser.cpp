#include "color/cube_parser.hpp"

#include <cmath>
#include <cstdlib>
#include <limits>
#include <sstream>
#include <vector>

namespace color {

namespace {

constexpr std::size_t kMaxLineLength = 65536;

bool isSpace(char ch) { return ch == ' ' || ch == '\t' || ch == '\r' || ch == '\f' || ch == '\v'; }

std::string trim(const std::string& text) {
    std::size_t begin = 0;
    while (begin < text.size() && isSpace(text[begin])) ++begin;
    std::size_t end = text.size();
    while (end > begin && isSpace(text[end - 1])) --end;
    return text.substr(begin, end - begin);
}

std::vector<std::string> tokenize(const std::string& line) {
    std::vector<std::string> tokens;
    std::size_t i = 0;
    while (i < line.size()) {
        while (i < line.size() && isSpace(line[i])) ++i;
        if (i >= line.size()) break;
        std::size_t start = i;
        while (i < line.size() && !isSpace(line[i])) ++i;
        tokens.push_back(line.substr(start, i - start));
    }
    return tokens;
}

std::string upper(std::string text) {
    for (char& ch : text) {
        if (ch >= 'a' && ch <= 'z') ch = static_cast<char>(ch - 'a' + 'A');
    }
    return text;
}

// strtod accepts "nan" and "inf"; the format does not, and neither do we.
bool parseNumber(const std::string& token, double& out) {
    if (token.empty()) return false;
    char* end = nullptr;
    errno = 0;
    const double value = std::strtod(token.c_str(), &end);
    if (end != token.c_str() + token.size()) return false;
    if (errno == ERANGE || !std::isfinite(value)) return false;
    out = value;
    return true;
}

void requireTokenCount(const std::string& source, std::size_t line,
                       const std::vector<std::string>& tokens, std::size_t expected,
                       const char* keyword) {
    if (tokens.size() != expected + 1) {
        throw CubeError(source, line, std::string(keyword) + " expects " +
                                        std::to_string(expected) + " value(s), got " +
                                        std::to_string(tokens.size() - 1));
    }
}

// Parses tokens[first..end). `first` is 1 for keyword lines (skipping the
// keyword) and 0 for bare sample lines.
std::vector<double> parseNumbers(const std::string& source, std::size_t line,
                                 const std::vector<std::string>& tokens, std::size_t first,
                                 const char* keyword) {
    std::vector<double> values;
    values.reserve(tokens.size() - first);
    for (std::size_t i = first; i < tokens.size(); ++i) {
        double value = 0.0;
        if (!parseNumber(tokens[i], value)) {
            throw CubeError(source, line, std::string(keyword) + " has a non-finite or " +
                                            "malformed number: " + tokens[i]);
        }
        values.push_back(value);
    }
    return values;
}

int parseGridSize(const std::string& source, std::size_t line, const std::string& token) {
    if (token.empty()) {
        throw CubeError(source, line, "LUT_3D_SIZE has an empty dimension");
    }
    for (char ch : token) {
        if (ch < '0' || ch > '9') {
            throw CubeError(source, line,
                            "LUT_3D_SIZE dimension must be a positive integer: " + token);
        }
    }
    errno = 0;
    const long value = std::strtol(token.c_str(), nullptr, 10);
    if (errno == ERANGE || value < kMinLutSize || value > kMaxLutSize) {
        throw CubeError(source, line, "LUT_3D_SIZE dimension out of range [2,65]: " + token);
    }
    return static_cast<int>(value);
}

RGB reorder(const RGB& sample, bool bgr) {
    if (!bgr) return sample;
    return {sample.b, sample.g, sample.r};
}

void appendSample(LUT& lut, const RGB& sample, bool bgr, std::size_t line,
                  const std::string& source) {
    if (lut.data.size() >= lut.sampleCount()) {
        throw CubeError(source, line, "too many samples: LUT_3D_SIZE declares " +
                                          std::to_string(lut.sampleCount()));
    }
    lut.data.push_back(reorder(sample, bgr));
}

}  // namespace

CubeError::CubeError(const std::string& source, std::size_t line, const std::string& message)
    : LutError(source + ":" + std::to_string(line) + ": " + message),
      source_(source),
      line_(line),
      detail_(message) {}

LUT parseCubeText(const std::string& text, const std::string& source) {
    std::istringstream stream(text);
    return parseCube(stream, source);
}

LUT parseCube(std::istream& input, const std::string& source) {
    LUT lut;
    bool haveSize = false;
    bool haveTitle = false;
    bool haveDomainMin = false;
    bool haveDomainMax = false;
    bool haveOrder = false;
    bool bgr = false;
    std::size_t lineNumber = 0;
    std::size_t lastLine = 1;
    std::string raw;

    while (std::getline(input, raw)) {
        ++lineNumber;
        lastLine = lineNumber;
        if (raw.size() > kMaxLineLength) {
            throw CubeError(source, lineNumber, "line exceeds 65536 bytes");
        }
        if (lineNumber == 1 && raw.size() >= 3 && static_cast<unsigned char>(raw[0]) == 0xEF &&
            static_cast<unsigned char>(raw[1]) == 0xBB &&
            static_cast<unsigned char>(raw[2]) == 0xBF) {
            raw.erase(0, 3);
        }
        if (!raw.empty() && raw.back() == '\r') raw.pop_back();
        // Trailing comments are common in hand-edited .cube files.
        const std::size_t comment = raw.find('#');
        if (comment != std::string::npos) raw.erase(comment);
        const std::string line = trim(raw);
        if (line.empty()) continue;

        std::vector<std::string> tokens = tokenize(line);
        const std::string keyword = upper(tokens[0]);

        if (keyword == "TITLE") {
            if (haveTitle) throw CubeError(source, lineNumber, "duplicate TITLE");
            if (tokens.size() < 2) throw CubeError(source, lineNumber, "TITLE is empty");
            // TITLE is metadata only; it carries no parsing semantics.
            haveTitle = true;
            continue;
        }
        if (keyword == "LUT_3D_SIZE") {
            if (haveSize) throw CubeError(source, lineNumber, "duplicate LUT_3D_SIZE");
            if (!lut.data.empty()) {
                throw CubeError(source, lineNumber, "LUT_3D_SIZE must precede samples");
            }
            if (tokens.size() == 2) {
                const int n = parseGridSize(source, lineNumber, tokens[1]);
                lut.nx = lut.ny = lut.nz = n;
            } else if (tokens.size() == 4) {
                lut.nx = parseGridSize(source, lineNumber, tokens[1]);
                lut.ny = parseGridSize(source, lineNumber, tokens[2]);
                lut.nz = parseGridSize(source, lineNumber, tokens[3]);
            } else {
                throw CubeError(source, lineNumber,
                                "LUT_3D_SIZE expects one size or three dimensions");
            }
            haveSize = true;
            lut.data.reserve(lut.sampleCount());
            continue;
        }
        if (keyword == "LUT_1D_SIZE" || keyword == "LUT_1D_INPUT_RANGE") {
            throw CubeError(source, lineNumber,
                            keyword + " is not supported; only 3D LUTs are accepted");
        }
        if (keyword == "LUT_3D_ORDER") {
            if (haveOrder) throw CubeError(source, lineNumber, "duplicate LUT_3D_ORDER");
            if (!lut.data.empty()) {
                throw CubeError(source, lineNumber, "LUT_3D_ORDER must precede samples");
            }
            requireTokenCount(source, lineNumber, tokens, 1, "LUT_3D_ORDER");
            const std::string order = upper(tokens[1]);
            if (order == "RGB") {
                bgr = false;
            } else if (order == "BGR") {
                bgr = true;
            } else {
                throw CubeError(source, lineNumber, "LUT_3D_ORDER must be RGB or BGR: " +
                                                       tokens[1]);
            }
            haveOrder = true;
            continue;
        }
        if (keyword == "DOMAIN_MIN" || keyword == "DOMAIN_MAX") {
            requireTokenCount(source, lineNumber, tokens, 3, keyword.c_str());
            if (!lut.data.empty()) {
                throw CubeError(source, lineNumber, keyword + " must precede samples");
            }
            const std::vector<double> values = parseNumbers(source, lineNumber, tokens, 1, "domain");
            RGB& target = keyword == "DOMAIN_MIN" ? lut.domain.min : lut.domain.max;
            bool& flag = keyword == "DOMAIN_MIN" ? haveDomainMin : haveDomainMax;
            if (flag) throw CubeError(source, lineNumber, "duplicate " + keyword);
            flag = true;
            target = {values[0], values[1], values[2]};
            continue;
        }

        // Anything else is a sample: exactly three finite numbers. A sample
        // line cannot precede LUT_3D_SIZE because the count is unknown.
        if (!haveSize) {
            throw CubeError(source, lineNumber,
                            "sample or statement before LUT_3D_SIZE: " + tokens[0]);
        }
        if (tokens.size() != 3) {
            throw CubeError(source, lineNumber,
                            "unrecognised statement '" + tokens[0] +
                                "' or malformed sample (expected 3 values)");
        }
        std::vector<double> values = parseNumbers(source, lineNumber, tokens, 0, "sample");
        appendSample(lut, {values[0], values[1], values[2]}, bgr, lineNumber, source);
    }

    if (input.bad()) {
        throw CubeError(source, lineNumber + 1, "stream read failure");
    }
    if (!haveSize) {
        throw CubeError(source, lastLine, "missing LUT_3D_SIZE");
    }
    if (lut.data.size() != lut.sampleCount()) {
        throw CubeError(source, lastLine + 1,
                        "sample count mismatch: LUT_3D_SIZE requires " +
                            std::to_string(lut.sampleCount()) + ", got " +
                            std::to_string(lut.data.size()));
    }
    if (!lut.domain.valid()) {
        throw CubeError(source, lastLine,
                        "domain width must be positive and finite in every channel");
    }
    return lut;
}

std::string serializeCube(const LUT& lut, const std::string& title) {
    validateLut(lut);
    std::ostringstream out;
    out.precision(9);
    if (!title.empty()) out << "TITLE \"" << title << "\"\n";
    out << "LUT_3D_SIZE " << lut.nx << " " << lut.ny << " " << lut.nz << "\n";
    out << "DOMAIN_MIN " << lut.domain.min.r << " " << lut.domain.min.g << " "
        << lut.domain.min.b << "\n";
    out << "DOMAIN_MAX " << lut.domain.max.r << " " << lut.domain.max.g << " "
        << lut.domain.max.b << "\n";
    for (int z = 0; z < lut.nz; ++z) {
        for (int y = 0; y < lut.ny; ++y) {
            for (int x = 0; x < lut.nx; ++x) {
                const RGB& sample = lut.at(x, y, z);
                out << sample.r << " " << sample.g << " " << sample.b << "\n";
            }
        }
    }
    return out.str();
}

}  // namespace color
