#!/bin/sh
# Builds "amarit demo.app" 1.0.0 here, signed ad hoc.
set -eu
cd "$(dirname "$0")"
APP="amarit demo.app"
rm -rf "$APP" && mkdir -p "$APP/Contents/MacOS"
go build -o "$APP/Contents/MacOS/macupdate" .
cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>macupdate</string>
<key>CFBundleIdentifier</key><string>io.github.sudiptadeb.amarit.demo</string>
<key>CFBundleName</key><string>amarit demo</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>1.0.0</string>
<key>CFBundleVersion</key><string>1</string>
<key>NSPrincipalClass</key><string>NSApplication</string>
</dict></plist>
PLIST
codesign --force --sign - "$APP"
echo "built $(pwd)/$APP"
