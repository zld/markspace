#include "subtitle_io.hpp"

#include <fstream>
#include <set>
#include <sstream>

#include <nlohmann/json.hpp>

#include "utf8.hpp"

namespace sm {

namespace {

bool validateUtf8Text(const std::string& text, std::size_t entry_pos,
                      Error& error) {
    std::vector<Codepoint> cps;
    Utf8Error utf_err;
    if (!decodeUtf8(text, cps, utf_err)) {
        std::ostringstream os;
        os << "entry " << entry_pos
           << ": text is not valid UTF-8 at byte "
           << utf_err.byte_offset << " (" << utf_err.message << ")";
        error = {ErrorKind::Config, os.str()};
        return false;
    }
    return true;
}

}  // namespace

bool loadSubtitles(const std::string& path,
                   std::vector<SubtitleInput>& out, Error& error) {
    std::ifstream in(path, std::ios::binary);
    if (!in) {
        error = {ErrorKind::Config,
                 "cannot open input file: " + path};
        return false;
    }
    nlohmann::json doc;
    try {
        in >> doc;
    } catch (const std::exception& e) {
        error = {ErrorKind::Config,
                 std::string("invalid JSON input: ") + e.what()};
        return false;
    }
    if (!doc.is_object() || !doc.contains("subtitles")) {
        error = {ErrorKind::Config,
                 R"(input must be an object with a "subtitles" array)"};
        return false;
    }
    const auto& arr = doc["subtitles"];
    if (!arr.is_array()) {
        error = {ErrorKind::Config,
                 R"("subtitles" must be an array)"};
        return false;
    }
    if (arr.size() > static_cast<std::size_t>(kMaxSubtitles)) {
        error = {ErrorKind::Config,
                 "subtitle count " + std::to_string(arr.size()) +
                     " exceeds limit " + std::to_string(kMaxSubtitles)};
        return false;
    }

    std::set<std::string> seen_ids;
    out.reserve(arr.size());
    for (std::size_t i = 0; i < arr.size(); ++i) {
        const std::size_t pos = i + 1;  // 1-based for user messages
        const auto& item = arr[i];
        if (!item.is_object()) {
            error = {ErrorKind::Config,
                     "entry " + std::to_string(pos) +
                         ": must be a JSON object"};
            return false;
        }
        if (!item.contains("id") || !item["id"].is_string()) {
            error = {ErrorKind::Config,
                     "entry " + std::to_string(pos) +
                         R"(: "id" must be a string)"};
            return false;
        }
        if (!item.contains("text") || !item["text"].is_string()) {
            error = {ErrorKind::Config,
                     "entry " + std::to_string(pos) +
                         R"(: "text" must be a string)"};
            return false;
        }
        SubtitleInput si;
        si.id = item["id"].get<std::string>();
        if (si.id.empty()) {
            error = {ErrorKind::Config,
                     "entry " + std::to_string(pos) +
                         R"(: "id" must not be empty)"};
            return false;
        }
        if (seen_ids.count(si.id) != 0) {
            error = {ErrorKind::Config,
                     "entry " + std::to_string(pos) +
                         ": duplicate id \"" + si.id + "\""};
            return false;
        }
        seen_ids.insert(si.id);

        si.text = item["text"].get<std::string>();
        if (si.text.size() >
            static_cast<std::size_t>(kMaxSubtitleBytes)) {
            error = {ErrorKind::Config,
                     "entry " + std::to_string(pos) +
                         ": text length exceeds " +
                         std::to_string(kMaxSubtitleBytes) + " bytes"};
            return false;
        }
        if (!validateUtf8Text(si.text, pos, error)) return false;

        if (item.contains("lang")) {
            if (!item["lang"].is_string()) {
                error = {ErrorKind::Config,
                         "entry " + std::to_string(pos) +
                             R"(: "lang" must be a string)"};
                return false;
            }
            si.language = item["lang"].get<std::string>();
        }
        out.push_back(std::move(si));
    }
    return true;
}

}  // namespace sm
