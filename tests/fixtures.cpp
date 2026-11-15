#include "fixtures.hpp"

namespace assaudit {

namespace {

const char* kHeader =
    "[Script Info]\n"
    "ScriptType: v4.00+\n"
    "PlayResX: 1920\n"
    "PlayResY: 1080\n"
    "WrapStyle: 0\n"
    "ScaledBorderAndShadow: yes\n"
    "\n"
    "[V4+ Styles]\n"
    "Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, "
    "OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, "
    "ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, "
    "MarginL, MarginR, MarginV, Encoding, Parent\n"
    "Style: Base,Arial,48,&H00FFFFFF,&H000000FF,&H00000000,&H64000000,0,0,0,"
    "0,100,100,0,0,1,2,1,2,30,30,40,1,\n"
    "Style: Narration,Helvetica,40,&H00E8F0FF,&H000000FF,&H00101010,"
    "&H80000000,0,1,0,0,100,100,0,0,1,1.5,1,2,40,40,30,1,Base\n"
    "Style: Emphasis,Times New Roman,56,&H00FFE08A,&H000000FF,&H00202020,"
    "&H64000000,-1,0,0,0,110,100,2,0,1,2.5,2,5,30,30,40,1,Narration\n"
    "Style: Caption,Verdana,36,&H00FFFFFF,&H000000FF,&H00000000,&HA0000000,"
    "0,0,1,0,100,100,0,0,3,1,0,2,20,20,20,1,Base\n"
    "Style: ScreenText,Courier,44,&H00C0FFC0,&H000000FF,&H00001000,"
    "&H80000000,0,1,0,0,100,100,1,0,1,2,1,7,25,25,35,1,Caption\n"
    "Style: Title,Georgia,72,&H0080C8FF,&H000000FF,&H00000020,&H64000000,-1,"
    "1,0,0,100,100,4,0,1,3,3,8,60,60,60,1,Emphasis\n"
    "Style: Small,Arial,28,&H00D0D0D0,&H000000FF,&H00000000,&H80000000,0,0,0,"
    "0,100,100,0,0,1,1,1,3,15,15,15,1,Base\n"
    "Style: Banner,Helvetica,52,&H00FF9090,&H000000FF,&H00200000,&H64000000,"
    "-1,0,1,0,100,100,0,0,1,2,2,5,50,50,50,1,\n"
    "\n"
    "[Events]\n"
    "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, "
    "Effect, Text\n";

}  // namespace

std::string generateDemoAss() {
  std::string s = kHeader;
  // Tag counts per event: see fixture design; totals 47 with 2 unknown.
  s += "Dialogue: 0,0:00:00.00,0:00:03.00,Title,,0,0,0,,Opening line {\\b1}"
       "{\\i1}Hello, world!\n";
  s += "Dialogue: 0,0:00:03.00,0:00:06.00,Emphasis,,0,0,0,,{\\fs64\\c&H"
       "00FFFF&}Bright {\\fnCourier}warning{\\r} now\n";
  s += "Dialogue: 0,0:00:06.00,0:00:09.00,Narration,,0,0,0,,{\\an8\\pos(960,"
       "120)}Top positioned {\\alpha&H80&}faded text\n";
  s += "Dialogue: 0,0:00:09.00,0:00:12.00,Caption,,0,0,0,,{\\move(100,900,"
       "1800,900)}Sliding caption {\\fscx120}wide\n";
  s += "Dialogue: 0,0:00:12.00,0:00:15.00,ScreenText,,0,0,0,,{\\t(0,500,\\fscy"
       "150\\bord4)}Growing {\\fsp6}spaced text\n";
  s += "Dialogue: 0,0:00:15.00,0:00:18.00,Small,,0,0,0,,Line one\\NLine two "
       "{\\u1}underlined{\\u0} end\n";
  s += "Dialogue: 0,0:00:18.00,0:00:21.00,Base,,0,0,0,,{\\b1\\b0}reset bold "
       "{\\zoo12}{\\fs40}sized again\n";
  s += "Dialogue: 0,0:00:21.00,0:00:24.00,Banner,,0,0,0,,{\\frz15\\blur3\\3c&"
       "H0000FF&}Rotated banner {\\qq99}glow\n";
  s += "Dialogue: 0,0:00:24.00,0:00:27.00,Title,,0,0,0,,{\\fnGeorgia\\fs80"
       "\\bord2}Grand title, {\\i1}italic phrase{\\r}\n";
  s += "Dialogue: 0,0:00:27.00,0:00:30.00,Caption,,0,0,0,,{\\pos(200,500)}"
       "Pinned {\\c&H00FF00&}green {\\c&H0000FF&}blue\n";
  s += "Dialogue: 0,0:00:30.00,0:00:33.00,Narration,,0,0,0,,Commas, in text, "
       "stay literal {\\an2\\bord2}bottom note\n";
  s += "Dialogue: 0,0:00:33.00,0:00:36.00,Emphasis,,0,0,0,,{\\t(100,400,\\fs"
       "70)}Pulse {\\shad3\\b1\\1a&H40&}shadow bold {\\i1}\n";
  return s;
}

namespace {

const char* kMiniStyles =
    "[V4+ Styles]\n"
    "Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, "
    "OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, "
    "ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, "
    "MarginL, MarginR, MarginV, Encoding, Parent\n"
    "Style: Base,Arial,40,&H00FFFFFF,&H000000FF,&H00000000,&H64000000,0,0,0,"
    "0,100,100,0,0,1,2,1,2,20,20,30,1,\n";

std::string miniEvent(const std::string& style, const std::string& start,
                      const std::string& end, const std::string& text) {
  return "Dialogue: 0," + start + "," + end + "," + style +
         ",,0,0,0,,," + text + "\n";
}

}  // namespace

std::string generateMissingParentAss() {
  std::string s = "[Script Info]\nPlayResX: 1280\nPlayResY: 720\n\n";
  s += kMiniStyles;
  s += "Style: Orphan,Arial,30,&H00FFFFFF,&H000000FF,&H00000000,"
       "&H64000000,0,0,0,0,100,100,0,0,1,1,1,2,10,10,10,1,Ghost\n\n"
       "[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, "
       "MarginV, Effect, Text\n";
  s += miniEvent("Orphan", "0:00:00.00", "0:00:02.00", "blocked by missing parent");
  s += miniEvent("Base", "0:00:00.00", "0:00:02.00", "{\\b1}still renders");
  return s;
}

std::string generateCycleAss() {
  std::string s = "[Script Info]\nPlayResX: 1280\nPlayResY: 720\n\n";
  s += kMiniStyles;
  s += "Style: Alpha,Arial,30,&H00FFFFFF,&H000000FF,&H00000000,"
       "&H64000000,0,0,0,0,100,100,0,0,1,1,1,2,10,10,10,1,Beta\n";
  s += "Style: Beta,Arial,30,&H00FFFFFF,&H000000FF,&H00000000,"
       "&H64000000,0,0,0,0,100,100,0,0,1,1,1,2,10,10,10,1,Alpha\n\n"
       "[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, "
       "MarginV, Effect, Text\n";
  s += miniEvent("Alpha", "0:00:00.00", "0:00:02.00", "cycle blocked");
  return s;
}

std::string generateDuplicateStyleAss() {
  std::string s = "[Script Info]\nPlayResX: 1280\nPlayResY: 720\n\n";
  s += kMiniStyles;
  s += "Style: Base,Times New Roman,99,&H00FF0000,&H000000FF,&H00000000,"
       "&H64000000,0,0,0,0,100,100,0,0,1,1,1,2,10,10,10,1,\n\n"
       "[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, "
       "MarginV, Effect, Text\n";
  s += miniEvent("Base", "0:00:00.00", "0:00:02.00", "duplicate name");
  return s;
}

std::string generateTimecodeBadAss() {
  std::string s = "[Script Info]\nPlayResX: 1280\nPlayResY: 720\n\n";
  s += kMiniStyles;
  s += "\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, "
       "MarginV, Effect, Text\n";
  s += miniEvent("Base", "0:00:05.00", "0:00:02.00", "end before start");
  s += miniEvent("Base", "0:00:02.00", "0:00:02.00", "zero duration");
  s += miniEvent("Base", "nonsense", "0:00:02.00", "bad timecode");
  s += miniEvent("Base", "0:00:02.00", "0:00:04.00", "{\\i1}healthy event");
  return s;
}

std::string generateUnclosedTagAss() {
  std::string s = "[Script Info]\nPlayResX: 1280\nPlayResY: 720\n\n";
  s += kMiniStyles;
  s += "\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, "
       "MarginV, Effect, Text\n";
  s += miniEvent("Base", "0:00:00.00", "0:00:02.00",
                 "{\\b1 unclosed {\\i1}next {\\fs(30}paren");
  return s;
}

std::string generateCommasAndEmptyAss() {
  std::string s = "[Script Info]\nPlayResX: 1280\nPlayResY: 720\n\n";
  s += kMiniStyles;
  s += "\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, "
       "MarginV, Effect, Text\n";
  s += "Dialogue: 0,0:00:00.00,0:00:02.00,Base,,0,0,0,,Text, with, "
       "commas {\\pos(10,20)}, and trailing,\n";
  s += "Comment: 0,0:00:00.00,0:00:02.00,Base,,0,0,0,,comment, ignored\n";
  return s;
}

}  // namespace assaudit
