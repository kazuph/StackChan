#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
APP_PATH="$HOME/Applications/StackChan IR Remote.app"
BINARY_PATH="/tmp/StackChanIRRemote"

cd "$REPO_ROOT"
swiftc tools/mac_ac_remote/StackChanIRRemote.swift -o "$BINARY_PATH"

/bin/rm -rf "$APP_PATH"
/bin/mkdir -p "$APP_PATH/Contents/MacOS"
/bin/cp "$BINARY_PATH" "$APP_PATH/Contents/MacOS/StackChanIRRemote"

/usr/libexec/PlistBuddy \
  -c "Clear dict" \
  -c "Add :CFBundleExecutable string StackChanIRRemote" \
  -c "Add :CFBundleIdentifier string com.kazuph.stackchan-ir-remote" \
  -c "Add :CFBundleName string StackChan IR Remote" \
  -c "Add :CFBundleDisplayName string StackChan IR Remote" \
  -c "Add :CFBundlePackageType string APPL" \
  -c "Add :CFBundleVersion string 1" \
  -c "Add :CFBundleShortVersionString string 1.0" \
  -c "Add :NSPrincipalClass string NSApplication" \
  -c "Add :LSMinimumSystemVersion string 13.0" \
  "$APP_PATH/Contents/Info.plist" >/dev/null

if pids="$(pgrep -x StackChanIRRemote 2>/dev/null)"; then
  kill $pids 2>/dev/null || true
  sleep 0.5
fi
pkill -f "tools/mac_ac_remote/ac_ir_tool.py watch-mcp-ir" 2>/dev/null || true
/usr/bin/open "$APP_PATH"
