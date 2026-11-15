// tests/fixtures.cpp — deterministic adversarial subtitle samples for
// offline tests. This file is a test-side generator: it produces the
// malformed and edge-case inputs that the binary-side fixture writer
// (src/fixture.cpp) intentionally excludes. It is built as a tiny
// standalone helper and never linked into the shipped executable.
//
// Build:
//   c++ -std=c++17 -I src tests/fixtures.cpp -o build/fixture-samples
// Run with an output directory; each sample is one JSON file.
#include <cstdio>
#include <fstream>
#include <string>

namespace {

void write(const std::string& dir, const std::string& name,
           const std::string& bytes) {
    std::ofstream out(dir + "/" + name,
                      std::ios::binary | std::ios::trunc);
    out << bytes;
    if (!out) {
        std::fprintf(stderr, "failed writing %s/%s\n", dir.c_str(),
                     name.c_str());
        std::exit(1);
    }
}

}  // namespace

int main(int argc, char** argv) {
    if (argc != 2) {
        std::fputs("usage: fixture-samples <output-dir>\n", stderr);
        return 2;
    }
    const std::string dir = argv[1];

    // Duplicate identifier (must be rejected with the entry position).
    write(dir, "duplicate_id.json",
          "{\"subtitles\":[{\"id\":\"dup\",\"text\":\"a\"},"
          "{\"id\":\"dup\",\"text\":\"b\"}]}");

    // Field type errors: numeric id, non-string text, non-string lang.
    write(dir, "bad_id_type.json",
          "{\"subtitles\":[{\"id\":42,\"text\":\"a\"}]}");
    write(dir, "bad_text_type.json",
          "{\"subtitles\":[{\"id\":\"x\",\"text\":true}]}");
    write(dir, "bad_lang_type.json",
          "{\"subtitles\":[{\"id\":\"x\",\"text\":\"a\",\"lang\":9}]}");

    // Malformed JSON envelope.
    write(dir, "broken_json.json", "{ this is not json");
    write(dir, "wrong_shape.json", "{\"items\":[]}");

    // Valid JSON envelope but an illegal raw UTF-8 content byte (0xFF).
    {
        std::string doc =
            "{\"subtitles\":[{\"id\":\"x\",\"text\":\"bad";
        doc.push_back(static_cast<char>(0xFF));
        doc += "\"}]}";
        write(dir, "invalid_utf8.json", doc);
    }

    // A truncated multibyte sequence at the end of the text.
    {
        std::string doc =
            "{\"subtitles\":[{\"id\":\"x\",\"text\":\"end";
        doc.push_back(static_cast<char>(0xE4));
        doc += "\"}]}";
        write(dir, "truncated_utf8.json", doc);
    }

    // Valid JSON that decodes a lone surrogate via an escape sequence;
    // the envelope parses and our own strict UTF-8 decoder must reject it.
    write(dir, "surrogate_escape.json",
          std::string(
              "{\"subtitles\":[{\"id\":\"x\",\"text\":\"x") +
              "\\ud800" + "\"}]}");

    // A combining mark delivered through a JSON escape: the envelope is
    // valid and our segmenter must keep base + mark in one cluster.
    write(dir, "combining_escape.json",
          std::string(
              "{\"subtitles\":[{\"id\":\"x\",\"text\":\"e") +
              "\\u0301" + "\"}]}");

    // An overlong UTF-8 encoding of '/' (raw bytes C0 AF).
    write(dir, "overlong_utf8.json",
          std::string(
              "{\"subtitles\":[{\"id\":\"x\",\"text\":\"x\xC0\xAF\"}]}"));

    // A UTF-16 surrogate encoded directly in UTF-8 (U+D800).
    write(dir, "surrogate_utf8.json",
          std::string(
              "{\"subtitles\":[{\"id\":\"x\",\"text\":\"x\xED\xA0\x80\"}]}"));

    // Unbreakable long identifier that cannot fit any reasonable width.
    write(dir, "long_identifier.json",
          "{\"subtitles\":[{\"id\":\"long\","
          "\"text\":\"BUILD_TASK_7F3A9C2E41B8D605A4E29C81F3D7B6E029"
          "A54C81D7F300112233445566778899AABBCCDDEEFF\"}]}");

    // Empty collection and a single valid empty-text subtitle.
    write(dir, "empty_array.json", "{\"subtitles\":[]}");
    write(dir, "empty_text.json",
          "{\"subtitles\":[{\"id\":\"empty\",\"text\":\"\"}]}");

    std::printf("wrote 14 adversarial samples to %s\n", dir.c_str());
    return 0;
}
