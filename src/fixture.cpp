#include "subtitle_io.hpp"

#include <fstream>
#include <vector>

#include <nlohmann/json.hpp>

namespace sm {

using json = nlohmann::json;

namespace {

// All literal bytes are embedded in the binary; no external sample files.
json fixtureDocument() {
    struct Row { const char* id; const char* lang; std::string text; };
    const std::string zwj_guitar =
        "\xF0\x9F\x91\xA8" "\xE2\x80\x8D" "\xF0\x9F\x8E\xB8";  // man ZWJ guitar
    const std::string flag =
        "\xF0\x9F\x87\xA8" "\xF0\x9F\x87\xBA";                 // regional pair
    const std::string heart_vs =
        "\xE2\x9D\xA4" "\xEF\xB8\x8F";                         // heart VS16
    const std::string e_acute = std::string("releve") + "e" +
                                "\xCC\x81";                     // combining acute

    std::vector<Row> rows = {
        {"s01", "en", "The quick brown fox jumps over the lazy dog."},
        {"s02", "zh",
         "\xE4\xBD\xA0\xE5\xA5\xBD\xEF\xBC\x8C\xE4\xB8\x96\xE7\x95\x8C\xE3\x80\x82"},
        {"s03", "ja",
         "\xE3\x81\x93\xE3\x82\x93\xE3\x81\xAB\xE3\x81\xA1\xE3\x81\xAF\xE3\x80\x81\xE4\xB8\x96\xE7\x95\x8C\xE3\x80\x82"},
        {"s04", "ko",
         "\xEC\x95\x88\xEB\x85\x95\xED\x95\x98\xEC\x84\xB8\xEC\x9A\x94"},
        {"s05", "en", "Mixed 中英混排 with ASCII and 全角标点（括号）。"},
        {"s06", "fr", e_acute.c_str()},
        {"s07", "en",
         ("Emoji sequence " + zwj_guitar + " stays atomic " + flag + " " +
          heart_vs)},
        {"s08", "en", "Hard\nexplicit\nnewline\nlines."},
        {"s09", "", ""},                       // empty text -> zero box
        {"s10", "en", std::string(180, 'W')},  // unbreakable long run
        {"s11", "en",
         "BUILD_TASK_7F3A9C2E41B8D605A4E29C81F3D7B6E029A54C81D7F3"},
        {"s12", "zh",
         "\xE5\x85\xA8\xE8\xA7\x92\xE6\xA0\x87\xE7\x82\xB9\xEF\xBC\x9A\xE3\x80\x90\xE6\xA0\x87\xE9\xA2\x98\xE3\x80\x91\xEF\xBC\x88\xE5\xAE\x8C\xEF\xBC\x89"},
        {"s13", "en", "Trailing space cluster trimming check   "},
        {"s14", "ar",
         "\xD8\xA7\xD9\x84\xD8\xB3\xD9\x84\xD8\xA7\xD9\x85 \xD8\xB9\xD9\x84\xD9\x89\xD9\x83\xD8\xA9"},
        {"s15", "ru",
         "\xD0\x9F\xD1\x80\xD0\xB8\xD0\xB2\xD0\xB5\xD1\x82, \xD0\xBC\xD0\xB8\xD1\x80"},
        {"s16", "en",
         "e\xCC\x81" "a\xCC\x80" "o\xCC\x88" " combining cluster test"},
        {"s17", "zh",
         std::string(60,
                     '\xE4')},  // invalid-looking placeholder replaced below
        {"s18", "en",
         "Numbers 1234567890 and symbols <>&\"' plus — dash … ellipsis"},
        {"s19", "en",
         ("Variation selector text style "
          "\xE2\x9C\x93" "\xEF\xB8\x8F" " vs emoji style "
          "\xE2\x9C\x93")},
        {"s20", "zh",
         "\xE7\xA6\x81\xE5\x88\x99\xE3\x80\x82\xE8\xA1\x8C\xE9\xA6\x96\xE7\xA6\x81\xE6\xAD\xA2\xE5\x87\xBA\xE7\x8E\xB0\xE5\x8F\xA5\xE5\x8F\xB7\xE3\x80\x89"},
    };
    // s17: 60 CJK characters (repeat 中) to force CJK line breaking.
    rows[16].text.clear();
    for (int i = 0; i < 60; ++i)
        rows[16].text += "\xE4\xB8\xAD";

    json arr = json::array();
    for (const auto& r : rows) {
        json item = {{"id", r.id}, {"text", r.text}};
        if (r.lang[0] != '\0') item["lang"] = r.lang;  // s09 omits it
        arr.push_back(item);
    }
    return json{{"subtitles", arr}};
}

}  // namespace

bool writeFixture(const std::string& path, Error& error) {
    const json doc = fixtureDocument();
    std::ofstream out(path, std::ios::binary | std::ios::trunc);
    if (!out) {
        error = {ErrorKind::Output,
                 "cannot open output for writing: " + path};
        return false;
    }
    try {
        out << doc.dump(2) << '\n';
    } catch (const std::exception& e) {
        error = {ErrorKind::Output,
                 std::string("failed writing fixture: ") + e.what()};
        return false;
    }
    if (!out) {
        error = {ErrorKind::Output,
                 "write failed (disk full or path not writable): " + path};
        return false;
    }
    return true;
}

}  // namespace sm
