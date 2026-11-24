#!/usr/bin/env bash
# Package the windowed shell as a macOS app bundle and a dmg.
#
# Why this is not the kernel's release path: desktop/ is a separate module that
# needs cgo and the platform WebView, so it cannot be cross-compiled the way the
# CLI is. One host builds one platform's artifacts; "all platforms" is a matrix
# of hosts, not a flag here. This script therefore refuses to run anywhere but
# Darwin instead of pretending GOOS=linux would work.
#
# What it does not do: notarization. Signing needs an Apple Developer identity
# and an app-specific password, both of which belong to the person shipping the
# build, so the script takes an identity with --sign and stops there. An
# unsigned dmg still runs, but Gatekeeper quarantines it on another machine.
#
# Artifacts land in go/dist/package/ (already ignored by the root .gitignore).
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../tests" && pwd)/lib.sh"

APP_NAME=atlas
BINARY=atlas-desktop
BUNDLE_ID=dev.atlas.desktop

VERSION=""
ARCH=""
UNIVERSAL=0
ICON=""
SIGN_ID=""
BUILD_WEB=0
OUT="$TREE_ROOT/dist/package"
# The script changes directory before it uses either path, so both are resolved
# against the shell the reader typed into rather than against go/.
CALLER_PWD="$PWD"

abs() {
	case "$1" in
	/*) printf '%s\n' "$1" ;;
	*) printf '%s/%s\n' "$CALLER_PWD" "$1" ;;
	esac
}

usage() {
	cat <<'USAGE'
usage: scripts/package.sh [options]

  --version <v>     version to stamp (default: the var in desktop/main.go)
  --arch <goarch>   build one architecture: arm64 or amd64 (default: host)
  --universal       build arm64 and amd64 and combine them with lipo
  --icon <png>      source image, converted to an .icns with sips and iconutil
                    (default: desktop/build/appicon.png when it exists)
  --sign <identity> codesign identity, e.g. "Developer ID Application: ...".
                    Without it the bundle is signed ad-hoc, which is enough to
                    launch locally but not to distribute.
  --web             rebuild web/ first (needs npm); otherwise the committed
                    dist embedded by //go:embed is what ships
  --out <dir>       artifact directory (default: go/dist/package)
  -h, --help        this text
USAGE
}

die() {
	fail "$1" || true
	exit 1
}

while (($#)); do
	case "$1" in
	--version)
		VERSION="${2:-}"
		[[ -n $VERSION ]] || die "--version needs a value"
		shift 2
		;;
	--arch)
		ARCH="${2:-}"
		[[ -n $ARCH ]] || die "--arch needs a value"
		shift 2
		;;
	--universal)
		UNIVERSAL=1
		shift
		;;
	--icon)
		ICON="${2:-}"
		[[ -n $ICON ]] || die "--icon needs a path"
		ICON="$(abs "$ICON")"
		shift 2
		;;
	--sign)
		SIGN_ID="${2:-}"
		[[ -n $SIGN_ID ]] || die "--sign needs an identity"
		shift 2
		;;
	--web)
		BUILD_WEB=1
		shift
		;;
	--out)
		OUT="${2:-}"
		[[ -n $OUT ]] || die "--out needs a path"
		OUT="$(abs "$OUT")"
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		printf 'package: unknown argument %s\n\n' "$1" >&2
		usage >&2
		exit 1
		;;
	esac
done

step "package: host"
if [[ $(uname -s) != Darwin ]]; then
	die "this script only packages for macOS; run it on a Mac (uname -s says $(uname -s))"
fi
command -v hdiutil >/dev/null 2>&1 || die "hdiutil not found"
command -v codesign >/dev/null 2>&1 || die "codesign not found"
xcode-select -p >/dev/null 2>&1 || die "no Xcode command line tools; cgo cannot build the WebView half"
ok "$(sw_vers -productVersion) on $(uname -m)"

# Both of these are cheap to check now and expensive to discover after a
# thirteen-megabyte cgo build.
#
# The repository carries its own icon, so a plain run packages the real face
# instead of the generic one; --icon still overrides it.
if [[ -z $ICON && -f "$TREE_ROOT/desktop/build/appicon.png" ]]; then
	ICON="$TREE_ROOT/desktop/build/appicon.png"
fi
if [[ -n $ICON && ! -f $ICON ]]; then
	die "no such icon: $ICON"
fi
if [[ -n $SIGN_ID ]]; then
	if ! security find-identity -v -p codesigning 2>/dev/null | grep -qF "$SIGN_ID"; then
		die "no codesigning identity matching \"$SIGN_ID\" in the keychain"
	fi
fi

cd "$TREE_ROOT/desktop"

step "package: version"
if [[ -z $VERSION ]]; then
	# The default lives in the source so a plain `go build` and a packaged app
	# agree; read it instead of keeping a second copy here.
	VERSION="$(sed -n 's/^var version = "\(.*\)"$/\1/p' main.go)"
	[[ -n $VERSION ]] || die "could not read the default version out of desktop/main.go; pass --version"
fi
[[ $VERSION =~ ^[0-9]+(\.[0-9]+)*$ ]] || die "version must be numeric and dot-separated: $VERSION"
ok "version $VERSION"

step "package: architecture"
if ((UNIVERSAL)); then
	ARCHES=(arm64 amd64)
	ARCH_TAG=universal
	command -v lipo >/dev/null 2>&1 || die "lipo not found"
elif [[ -n $ARCH ]]; then
	case "$ARCH" in
	arm64 | amd64) ;;
	*) die "--arch takes arm64 or amd64, not $ARCH" ;;
	esac
	ARCHES=("$ARCH")
	ARCH_TAG="$ARCH"
else
	ARCHES=("$(go env GOARCH)")
	ARCH_TAG="${ARCHES[0]}"
fi
ok "building for ${ARCHES[*]}"

if ((BUILD_WEB)); then
	step "package: frontend"
	command -v npm >/dev/null 2>&1 || die "--web needs npm in PATH"
	(cd "$TREE_ROOT/web" && npm run build >/dev/null)
	ok "web built into internal/httpapi/web/dist"
else
	step "package: frontend"
	ok "using the committed dist under internal/httpapi/web/dist"
fi

step "package: deployment target"
# The floor is a property of the toolchain, not a number worth hardcoding: Go
# stamps its own minimum into every object it emits, and the SDK stamps a newer
# one into the C/ObjC objects unless it is told otherwise. Building a two-line
# program and reading its LC_BUILD_VERSION is cheaper than keeping a constant
# that silently goes stale the next time Go drops an older macOS — and a plist
# that claims 11.0 while the binary needs 13.0 is a lie the reader pays for.
STAGE="$(mktemp -d "${TMPDIR:-/tmp}/atlas-package.XXXXXX")"
trap 'rm -rf "$STAGE"' EXIT

command -v vtool >/dev/null 2>&1 || die "vtool not found (it ships with the command line tools)"
probe="$STAGE/probe"
mkdir -p "$probe"
printf 'package main\n\nfunc main() {}\n' >"$probe/main.go"
(cd "$probe" && go mod init probe >/dev/null 2>&1 && CGO_ENABLED=0 go build -o probe .)
MIN_MACOS="$(vtool -show-build "$probe/probe" | awk '/minos/ {print $2; exit}')"
[[ -n $MIN_MACOS ]] || die "could not read the toolchain's minimum macOS; vtool said nothing"
ok "Go's minimum macOS is $MIN_MACOS"

step "package: go build"
# -s -w drop the symbol table and DWARF: a shipped window does not need them.
LDFLAGS="-s -w -X main.version=$VERSION -extldflags=-mmacosx-version-min=$MIN_MACOS"
BINARIES=()
for arch in "${ARCHES[@]}"; do
	out="$STAGE/$BINARY-$arch"
	# CGO_ENABLED is explicit: with it unset the WebView package reports "build
	# constraints exclude all Go files" instead of "cgo is off".
	#
	# The floor has to be stated in three places to get a quiet build: the C and
	# ObjC flags hold those objects to it, and extldflags holds the link step.
	# Left alone, Go compiles its own objects at the toolchain's minimum while
	# the SDK stamps the C ones at the SDK version, and ld warns once per object
	# ("built for newer 'macOS' version than being linked") — harmless, since
	# only the linked minos is enforced, but it buries a clean build in noise.
	# Enable private_mac_apis so WKWebView draws transparent over MacBackdropTranslucent.
	CGO_ENABLED=1 GOARCH="$arch" \
		CGO_CFLAGS="-mmacosx-version-min=$MIN_MACOS" \
		CGO_OBJCFLAGS="-mmacosx-version-min=$MIN_MACOS" \
		go build -tags private_mac_apis -trimpath -ldflags "$LDFLAGS" -o "$out" .
	ok "built $arch ($(du -h "$out" | awk '{print $1}'))"
	BINARIES+=("$out")
done

BIN="$STAGE/$BINARY"
if ((${#BINARIES[@]} > 1)); then
	lipo -create "${BINARIES[@]}" -output "$BIN"
	ok "lipo combined $(lipo -archs "$BIN")"
else
	cp "${BINARIES[0]}" "$BIN"
fi

step "package: app bundle"
# The image root is its own directory: hdiutil copies everything under the
# -srcfolder it is given, and the per-arch binaries plus the .iconset must not
# end up in the mounted volume.
DMGROOT="$STAGE/dmg"
APP="$DMGROOT/$APP_NAME.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$BIN" "$APP/Contents/MacOS/$BINARY"
chmod 755 "$APP/Contents/MacOS/$BINARY"

if [[ -n $ICON ]]; then
	command -v iconutil >/dev/null 2>&1 || die "iconutil not found"
	width="$(sips -g pixelWidth "$ICON" 2>/dev/null | awk '/pixelWidth/ {print $2}')"
	if [[ -n $width ]] && ((width < 1024)); then
		# iconutil accepts a smaller source, but the 512@2x slot then holds an
		# upscale and looks soft in the Dock.
		printf '    %snote%s icon is %spx wide; 1024 is what the Dock actually uses\n' \
			"$C_BOLD" "$C_RESET" "$width" >&2
	fi
	iconset="$STAGE/appicon.iconset"
	mkdir -p "$iconset"
	for base in 16 32 128 256 512; do
		sips -z "$base" "$base" "$ICON" --out "$iconset/icon_${base}x${base}.png" >/dev/null
		sips -z "$((base * 2))" "$((base * 2))" "$ICON" --out "$iconset/icon_${base}x${base}@2x.png" >/dev/null
	done
	iconutil -c icns "$iconset" -o "$APP/Contents/Resources/appicon.icns"
	ok "icon converted from $ICON"
	ICON_KEYS="	<key>CFBundleIconFile</key><string>appicon</string>"
else
	ICON_KEYS=""
	printf '    %snote%s no --icon: the bundle gets the generic application icon\n' \
		"$C_BOLD" "$C_RESET" >&2
fi

# NSAllowsLocalNetworking: the window is a WebView pointed at http://127.0.0.1
# on a port the same process just bound. Without it App Transport Security
# treats the loopback origin as insecure remote content.
cat >"$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key><string>zh_CN</string>
	<key>CFBundleExecutable</key><string>$BINARY</string>
	<key>CFBundleIdentifier</key><string>$BUNDLE_ID</string>
	<key>CFBundleName</key><string>Atlas</string>
	<key>CFBundleDisplayName</key><string>Atlas</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>$VERSION</string>
	<key>CFBundleVersion</key><string>$VERSION</string>
	<key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
	<key>LSMinimumSystemVersion</key><string>$MIN_MACOS</string>
	<key>NSHighResolutionCapable</key><true/>
	<key>NSAppTransportSecurity</key><dict><key>NSAllowsLocalNetworking</key><true/></dict>
$ICON_KEYS
</dict>
</plist>
PLIST
plutil -lint "$APP/Contents/Info.plist" >/dev/null
ok "Info.plist validated"

step "package: codesign"
if [[ -n $SIGN_ID ]]; then
	# --options runtime is the hardened runtime that notarization requires.
	codesign --force --options runtime --timestamp --sign "$SIGN_ID" "$APP"
	ok "signed with $SIGN_ID"
else
	# Go already ad-hoc signs darwin/arm64 binaries, but lipo rewrites the
	# Mach-O and invalidates that signature; an arm64 Mac refuses to launch the
	# result. Re-signing the bundle covers both cases.
	codesign --force --sign - "$APP"
	ok "signed ad-hoc (local use only)"
fi
codesign --verify --deep --strict "$APP"
ok "signature verified"

step "package: dmg"
# The artifacts directory is gitignored, and the guard keeps a typo in --out
# from turning the cleanup below into a deletion somewhere that matters.
[[ $OUT != / && -n $OUT ]] || die "refusing to write artifacts into $OUT"
mkdir -p "$OUT"
rm -rf "$OUT/$APP_NAME.app"
# ditto rather than cp: it preserves the code signature and extended attributes
# exactly, which cp does not promise.
ditto "$APP" "$OUT/$APP_NAME.app"
DMG="$OUT/$APP_NAME-$VERSION-$ARCH_TAG.dmg"
# The Applications symlink is what makes the mounted image a drag target
# instead of a folder the reader has to think about.
ln -s /Applications "$DMGROOT/Applications"
hdiutil create -quiet -volname "$APP_NAME $VERSION" -srcfolder "$DMGROOT" \
	-fs HFS+ -format UDZO -ov "$DMG"
hdiutil verify -quiet "$DMG"
ok "$(basename "$DMG") ($(du -h "$DMG" | awk '{print $1}'))"

step "package: done"
ok "app bundle $OUT/$APP_NAME.app"
ok "disk image $DMG"
if [[ -z $SIGN_ID ]]; then
	cat <<'NOTE'

The bundle is signed ad-hoc, so it launches here but Gatekeeper quarantines it
elsewhere: the reader has to right-click Open once, or drop the quarantine bit
with `xattr -dr com.apple.quarantine /Applications/atlas.app`. To ship it
properly, re-run with --sign "Developer ID Application: ..." and notarize:

  xcrun notarytool submit <dmg> --keychain-profile <profile> --wait
  xcrun stapler staple <dmg>
NOTE
fi
