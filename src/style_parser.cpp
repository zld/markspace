#include "style_parser.hpp"

#include <algorithm>
#include <cctype>
#include <sstream>

namespace asspipe {

std::string& trim(std::string& s) {
    auto not_space = [](unsigned char ch) { return std::isspace(ch) == 0; };
    s.erase(s.begin(), std::find_if(s.begin(), s.end(), not_space));
    s.erase(std::find_if(s.rbegin(), s.rend(), not_space).base(), s.end());
    return s;
}

std::string trim_copy(std::string s) { return trim(s); }

std::vector<std::string> split_fields(const std::string& line,
                                      std::size_t max_fields) {
    std::vector<std::string> fields;
    std::size_t start = 0;
    while (fields.size() + 1 < max_fields) {
        auto pos = line.find(',', start);
        if (pos == std::string::npos) break;
        fields.push_back(line.substr(start, pos - start));
        start = pos + 1;
    }
    fields.push_back(line.substr(start));
    return fields;
}

bool parse_timestamp(const std::string& token, long& out_centiseconds) {
    // H:MM:SS.cc — exactly two colons and one dot, numeric components.
    long h = 0, m = 0, s = 0, cs = 0;
    char c1 = 0, c2 = 0, c3 = 0;
    std::istringstream iss(token);
    iss >> h >> c1 >> m >> c2 >> s >> c3 >> cs;
    if (iss.fail() || !iss.eof() || c1 != ':' || c2 != ':' || c3 != '.')
        return false;
    if (m < 0 || m > 59 || s < 0 || s > 59 || cs < 0 || cs > 99 || h < 0)
        return false;
    out_centiseconds = ((h * 3600 + m * 60 + s) * 100) + cs;
    return true;
}

// Full section parsing is implemented in the parser batch; this keeps the
// first build linkable while the interface is fixed.
AssDocument parse_ass(const std::string&, const std::string&) {
    return AssDocument{};
}

ResolvedStyle resolve_style(const AssDocument&, const Style&) {
    return ResolvedStyle{};
}

} // namespace asspipe
