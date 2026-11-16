#include "tag_machine.hpp"

#include <cctype>
#include <set>

namespace subaudit {
namespace {

const std::set<std::string>& recognized_names() {
    static const std::set<std::string> names = {
        "b",     "i",    "u",   "s",    "bord", "xbord", "ybord", "shad",
        "xshad", "yshad", "be",  "blur", "fn",   "fs",    "fscx",  "fscy",
        "fsp",   "frx",  "fry", "frz",  "fr",   "fax",   "fay",   "fe",
        "c",     "1c",   "2c",  "3c",   "4c",   "alpha", "1a",    "2a",
        "3a",    "4a",   "an",  "a",    "k",    "kf",    "ko",    "q",
        "r",     "pos",  "move", "org",  "fad",  "fade",  "t",     "clip",
        "iclip", "p",    "pbo", "n",    "h"};
    return names;
}

// Scans a tag list within [begin, end). Recurses into \t(...) groups so
// nested transform tags are counted too.
void scan_block(const std::string& s, std::size_t begin, std::size_t end,
                TagStats& st) {
    std::size_t i = begin;
    while (i < end) {
        if (s[i] != '\\') {
            ++i;
            continue;
        }
        std::size_t j = i + 1;
        std::string name;
        while (j < end && std::isalnum(static_cast<unsigned char>(s[j])))
            name.push_back(
                static_cast<char>(std::tolower(static_cast<unsigned char>(s[j++]))));
        if (name.empty()) {
            ++i;
            continue;
        }
        if (recognized_names().count(name))
            st.add_recognized(name);
        else
            st.add_unknown(name);
        if (name == "t") {
            std::size_t p = j;
            while (p < end && std::isspace(static_cast<unsigned char>(s[p]))) ++p;
            if (p < end && s[p] == '(') {
                int depth = 1;
                std::size_t q = p + 1;
                while (q < end && depth > 0) {
                    if (s[q] == '(')
                        ++depth;
                    else if (s[q] == ')')
                        --depth;
                    ++q;
                }
                if (depth != 0) {
                    ++st.malformed;  // unclosed \t( group
                    return;
                }
                scan_block(s, p + 1, q - 1, st);
                i = q;
                continue;
            }
        }
        i = j;
    }
}

}  // namespace

void scan_event_text(const std::string& text, TagStats& st) {
    std::size_t i = 0;
    const std::size_t n = text.size();
    while (i < n) {
        const char ch = text[i];
        if (ch == '{') {
            const std::size_t close = text.find('}', i + 1);
            if (close == std::string::npos) {
                ++st.malformed;  // unclosed override block; rest is literal
                return;
            }
            scan_block(text, i + 1, close, st);
            i = close + 1;
        } else if (ch == '\\' && i + 1 < n &&
                   (text[i + 1] == 'N' || text[i + 1] == 'n' || text[i + 1] == 'h')) {
            st.add_recognized(std::string(
                1, static_cast<char>(std::tolower(static_cast<unsigned char>(text[i + 1])))));
            i += 2;
        } else {
            ++i;
        }
    }
}

}  // namespace subaudit
