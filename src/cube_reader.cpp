#include "lut/cube_reader.hpp"

#include <cmath>
#include <fstream>
#include <sstream>

#include "lut/io_util.hpp"

namespace lut {
namespace {

constexpr int kMinGrid = 2;
constexpr int kMaxGrid = 65;

std::string stripBom(std::string line) {
    static const std::string bom = "\xEF\xBB\xBF";
    if (line.rfind(bom, 0) == 0) {
        line.erase(0, bom.size());
    }
    return line;
}

std::string trim(std::string token) {
    while (!token.empty() &&
           (token.front() == ' ' || token.front() == '\t' ||
            token.front() == '\r')) {
        token.erase(token.begin());
    }
    while (!token.empty() &&
           (token.back() == ' ' || token.back() == '\t' ||
            token.back() == '\r')) {
        token.pop_back();
    }
    return token;
}

double parseFinite(const std::string& token, int line) {
    std::size_t consumed = 0;
    double value = 0.0;
    try {
        value = std::stod(token, &consumed);
    } catch (const std::exception&) {
        throw ParseError(line, "invalid numeric token: " + token);
    }
    if (consumed != token.size() || !std::isfinite(value)) {
        throw ParseError(line, "invalid numeric token: " + token);
    }
    return value;
}

}  // namespace

Lut3D readCube(const std::string& path) {
    std::ifstream in(path);
    if (!in) {
        throw IoError("cannot open cube file: " + path);
    }

    Lut3D lut;
    bool haveSize = false;
    bool haveMin = false;
    bool haveMax = false;
    int lineNo = 0;
    int expectedNodes = 0;

    std::string raw;
    while (std::getline(in, raw)) {
        ++lineNo;
        std::string line = trim(stripBom(raw));
        if (line.empty() || line[0] == '#') {
            continue;
        }
        std::istringstream ss(line);
        std::string keyword;
        ss >> keyword;

        if (keyword == "LUT_3D_SIZE") {
            if (haveSize) {
                throw ParseError(lineNo, "duplicate LUT_3D_SIZE");
            }
            std::string token;
            ss >> token;
            if (token.empty()) {
                throw ParseError(lineNo, "LUT_3D_SIZE requires a value");
            }
            const double rawSize = parseFinite(token, lineNo);
            if (rawSize < kMinGrid || rawSize > kMaxGrid ||
                rawSize != std::floor(rawSize)) {
                throw ParseError(lineNo,
                                 "LUT_3D_SIZE out of range [2,65]: " + token);
            }
            lut.size = static_cast<int>(rawSize);
            std::string extra;
            if (ss >> extra) {
                throw ParseError(lineNo,
                                 "unexpected token after LUT_3D_SIZE: " + extra);
            }
            haveSize = true;
        } else if (keyword == "DOMAIN_MIN" || keyword == "DOMAIN_MAX") {
            const bool isMin = keyword == "DOMAIN_MIN";
            if (isMin ? haveMin : haveMax) {
                throw ParseError(lineNo, "duplicate " + keyword);
            }
            for (int i = 0; i < 3; ++i) {
                std::string token;
                ss >> token;
                if (token.empty()) {
                    throw ParseError(lineNo, keyword + " needs 3 values");
                }
                (isMin ? lut.domainMin : lut.domainMax)[i] =
                    parseFinite(token, lineNo);
            }
            std::string extra;
            if (ss >> extra) {
                throw ParseError(lineNo,
                                 "unexpected token after " + keyword + ": " +
                                     extra);
            }
            (isMin ? haveMin : haveMax) = true;
        } else if (keyword == "LUT_1D_SIZE" || keyword == "LUT_1D_INPUT") {
            throw ParseError(lineNo, "unsupported directive: " + keyword);
        } else {
            // RGB data node: exactly three finite floats.
            if (!haveSize) {
                throw ParseError(lineNo,
                                 "data node before LUT_3D_SIZE: " + keyword);
            }
            if (expectedNodes == 0) {
                expectedNodes = lut.nodeCount();
                lut.nodes.reserve(static_cast<std::size_t>(expectedNodes));
            }
            std::string gToken, bToken;
            ss >> gToken >> bToken;
            if (gToken.empty() || bToken.empty()) {
                throw ParseError(lineNo, "node must contain R G B values");
            }
            RGB node{parseFinite(keyword, lineNo),
                     parseFinite(gToken, lineNo),
                     parseFinite(bToken, lineNo)};
            std::string extra;
            if (ss >> extra) {
                throw ParseError(lineNo, "unexpected token after RGB node");
            }
            if (static_cast<int>(lut.nodes.size()) >= expectedNodes) {
                throw ParseError(lineNo, "more than N^3 data nodes");
            }
            lut.nodes.push_back(node);
        }
    }

    if (in.bad()) {
        throw IoError("failure while reading cube file: " + path);
    }
    if (!haveSize) {
        throw LutDataError("missing LUT_3D_SIZE directive in " + path);
    }
    if (static_cast<int>(lut.nodes.size()) !=
        static_cast<int>(lut.nodeCount())) {
        throw LutDataError("expected " + std::to_string(lut.nodeCount()) +
                           " nodes, found " +
                           std::to_string(lut.nodes.size()) + " in " + path);
    }
    for (int i = 0; i < 3; ++i) {
        if (!(lut.domainMin[i] < lut.domainMax[i])) {
            throw LutDataError("invalid domain range in " + path);
        }
    }
    return lut;
}

std::string renderCube(const Lut3D& lut) {
    std::ostringstream out;
    out.precision(10);  // Round-trip error stays near double epsilon.
    out << "LUT_3D_SIZE " << lut.size << '\n';
    out << "DOMAIN_MIN " << lut.domainMin[0] << ' ' << lut.domainMin[1]
        << ' ' << lut.domainMin[2] << '\n';
    out << "DOMAIN_MAX " << lut.domainMax[0] << ' ' << lut.domainMax[1]
        << ' ' << lut.domainMax[2] << '\n';
    for (const RGB& node : lut.nodes) {
        out << node.r << ' ' << node.g << ' ' << node.b << '\n';
    }
    return out.str();
}

void writeCube(const std::string& path, const Lut3D& lut) {
    writeFileAtomic(path, renderCube(lut));
}

}  // namespace lut
