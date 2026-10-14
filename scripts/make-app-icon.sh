#!/usr/bin/env bash
# Regenerate DeskPixie's app icon from Bit's sprite sheet (mascots/bit/sheet.png).
#
# Usage (any directory):  scripts/make-app-icon.sh
#
# Renders the 1024×1024 master (scripts/make-app-icon.swift: idle frame 0, nearest-neighbour,
# on the macOS 824 px rounded-square grid) to mac/Icon/AppIcon-1024.png, then downsizes it with
# sips into mac/DeskPixie/Assets.xcassets/AppIcon.appiconset (16–512 pt, @1x and @2x). The app
# target uses it via ASSETCATALOG_COMPILER_APPICON_NAME=AppIcon (mac/project.yml). macOS tools
# only (swift, sips). Commit the regenerated files.
set -euo pipefail

cd "$(dirname "$0")/.."

PACK=mascots/bit
MASTER=mac/Icon/AppIcon-1024.png
SET=mac/DeskPixie/Assets.xcassets/AppIcon.appiconset

mkdir -p "$(dirname "$MASTER")" "$SET"
swift scripts/make-app-icon.swift "$PACK" "$MASTER"

# Point sizes; each gets an @1x and an @2x PNG.
SIZES=(16 32 128 256 512)
images=""
for pt in "${SIZES[@]}"; do
  for s in 1 2; do
    px=$((pt * s))
    suffix=""; [ "$s" = 1 ] || suffix="@2x"
    name="icon_${pt}x${pt}${suffix}.png"
    if [ "$px" = 1024 ]; then
      cp "$MASTER" "$SET/$name"
    else
      sips -z "$px" "$px" "$MASTER" --out "$SET/$name" >/dev/null
    fi
    images+="${images:+,
}    { \"idiom\" : \"mac\", \"size\" : \"${pt}x${pt}\", \"scale\" : \"${s}x\", \"filename\" : \"$name\" }"
  done
done

cat > "$SET/Contents.json" <<EOF
{
  "images" : [
$images
  ],
  "info" : { "author" : "xcode", "version" : 1 }
}
EOF
cat > "$(dirname "$SET")/Contents.json" <<EOF
{
  "info" : { "author" : "xcode", "version" : 1 }
}
EOF

echo "icon set: $SET"
ls "$SET"
