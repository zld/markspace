#!/bin/sh
# Generates the demo ASS fixture used by the acceptance run.
#
# Shape of the fixture:
#   - 5 unique style names: Base, DialogueKr, Signage, Legacy, Broken
#   - a duplicate "Base" definition, reported as a locatable diagnostic
#     without changing the style count
#   - 8 Dialogue events (one of them deliberately uses a missing font)
#   - 1 Comment line that must be ignored
set -eu

out="${1:-./build/generated/demo.ass}"
dir=$(dirname "$out")
mkdir -p "$dir"

cat > "$out.tmp" <<'ASS'
[Script Info]
ScriptType: v4.00+
PlayResX: 1920
PlayResY: 1080
WrapStyle: 0
ScaledBorderAndShadow: yes
YCbCr Matrix: None

[V4+ Styles]
Format: Name, Parent, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Base,,Helvetica,48,&H00FFFFFF,&H00FFFFFF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,0,2,60,60,50,1
Style: DialogueKr,Base,,,,,&H00FFD0D0,,,,,,,,,,,,,,,,80,
Style: Signage,DialogueKr,Avenir Next,,,,,,,,,1,,,,,,,,8,,,,
Style: Legacy,Base,Helvetica,40,,,,,0,,,,,,,,,500,0,2,,,,1
Style: Broken,Base,NoSuchFontXYZ,,,,,,,,,,,,,,,,,,,,,
Style: Base,,Helvetica,40,,,,,,,,,,,,,,,,,,,,

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:00.00,0:00:04.00,Base,Narration,0,0,0,,A plain first caption.
Dialogue: 0,0:00:04.00,0:00:08.00,DialogueKr,Voice,0,0,0,,{\i1}Inherited{\i0} style with {\c&H00FFFF&}colour{\c&HFFFFFF&} override.
Dialogue: 0,0:00:08.00,0:00:12.00,Signage,Sign,0,0,0,,{\an7\pos(120,90)\fs64\bord3}BIG SIGN\N{\fs32\shad2}second line
Dialogue: 0,0:00:12.00,0:00:16.00,Legacy,,0,0,0,,Unclosed override {\b1 stays bold
Dialogue: 0,0:00:16.00,0:00:20.00,Base,,0,0,0,,{\alpha&HFF&}Invisible caption
Dialogue: 0,0:00:20.00,0:00:24.00,Base,,0,0,0,,
Dialogue: 0,0:00:24.00,0:00:28.00,Base,,0,0,0,,{\rNoSuchStyle}Reset to a missing style
Dialogue: 0,0:00:28.00,0:00:32.00,Broken,,0,0,0,,Missing font fallback
Comment: 0,0:00:32.00,0:00:36.00,Base,,0,0,0,,This comment is ignored.
ASS

mv "$out.tmp" "$out"
printf 'wrote %s\n' "$out"
