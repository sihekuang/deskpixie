.PHONY: project build test run clean release verify-release

SWIFT_SOURCES := $(shell find DeskPixie -name '*.swift' -type f 2>/dev/null)

# Regenerate only when project.yml, the xcconfig or the set of app sources changes.
DeskPixie.xcodeproj/project.pbxproj: project.yml Configs/SharedSettings.xcconfig $(SWIFT_SOURCES)
	xcodegen generate

project: DeskPixie.xcodeproj/project.pbxproj

# A debug build is ad-hoc signed unless SIGNING_IDENTITY names a real one. Ad-hoc, every rebuild
# is a new app to macOS, which asks for the microphone again; signed, the permission is kept.
#   make build [SIGNING_IDENTITY="Developer ID Application: ..."]
BUILD_SIGN_FLAGS = $(if $(filter-out -,$(SIGNING_IDENTITY)),CODE_SIGN_STYLE=Manual CODE_SIGN_IDENTITY="$(SIGNING_IDENTITY)" DEVELOPMENT_TEAM="$(DEVELOPMENT_TEAM)",)

build: project
	xcodebuild -project DeskPixie.xcodeproj -scheme DeskPixie -configuration Debug -derivedDataPath build $(BUILD_SIGN_FLAGS) build

test:
	cd Packages/DeskPixieCore && swift test

run:
	open build/Build/Products/Debug/DeskPixie.app

# Release build, for local use and CI (.github/workflows/ci.yml, release.yml):
#   make release VERSION=1.2.3 [BUILD_NUMBER=42] [SIGNING_IDENTITY="Developer ID Application: ..."]
# SIGNING_IDENTITY defaults to ad-hoc (-). With a real identity every Mach-O (the app and the
# bundled libdeskpixie/libdf, re-signed by project.yml's scripts) gets the hardened runtime and
# a secure timestamp, as notarization requires. Output: $(RELEASE_APP).
VERSION ?=
BUILD_NUMBER ?= $(or $(GITHUB_RUN_NUMBER),1)
SIGNING_IDENTITY ?= -
DEVELOPMENT_TEAM ?=
RELEASE_APP := build/Build/Products/Release/DeskPixie.app
RELEASE_SIGN_FLAGS := $(if $(filter-out -,$(SIGNING_IDENTITY)),--timestamp,)

release: project
	@test -n "$(VERSION)" || { echo "usage: make release VERSION=x.y.z" >&2; exit 1; }
	xcodebuild -project DeskPixie.xcodeproj -scheme DeskPixie -configuration Release \
		-destination 'platform=macOS,arch=arm64' -derivedDataPath build \
		MARKETING_VERSION="$(VERSION)" CURRENT_PROJECT_VERSION="$(BUILD_NUMBER)" \
		CODE_SIGN_STYLE=Manual CODE_SIGN_IDENTITY="$(SIGNING_IDENTITY)" \
		DEVELOPMENT_TEAM="$(DEVELOPMENT_TEAM)" \
		ENABLE_HARDENED_RUNTIME=YES CODE_SIGN_INJECT_BASE_ENTITLEMENTS=NO \
		OTHER_CODE_SIGN_FLAGS="$(RELEASE_SIGN_FLAGS)" \
		build
	@$(MAKE) --no-print-directory verify-release

# Structural checks on the Release app (no launch): signature, bundled dylibs and resources
# (the app icon included), every @rpath dependency resolvable inside Contents/Frameworks,
# audio-input entitlement.
verify-release:
	@set -e; APP="$(RELEASE_APP)"; test -d "$$APP"; \
	codesign --verify --deep --strict --verbose=2 "$$APP"; \
	for f in Frameworks/libdeskpixie.dylib Frameworks/libdf.dylib Helpers/pix \
		Resources/DeepFilterNet3.tar.gz Resources/design-system Resources/pixies \
		Resources/Assets.car Resources/AppIcon.icns; do \
		test -e "$$APP/Contents/$$f" || { echo "error: missing Contents/$$f" >&2; exit 1; }; \
	done; \
	otool -l "$$APP/Contents/MacOS/DeskPixie" | grep -q '@executable_path/../Frameworks' \
		|| { echo "error: app rpath lacks @executable_path/../Frameworks" >&2; exit 1; }; \
	for bin in "$$APP/Contents/MacOS/DeskPixie" "$$APP/Contents/Frameworks/"*.dylib; do \
		for ref in $$(otool -L "$$bin" | tail -n +2 | awk '{print $$1}' | grep '^@rpath/' || true); do \
			name="$${ref#@rpath/}"; \
			case "$$bin" in *"/$$name") continue;; esac; \
			test -e "$$APP/Contents/Frameworks/$$name" \
				|| { echo "error: $$(basename "$$bin") -> $$ref not in Frameworks" >&2; exit 1; }; \
		done; \
	done; \
	"$$APP/Contents/Helpers/pix" version >/dev/null \
		|| { echo "error: Contents/Helpers/pix does not run" >&2; exit 1; }; \
	codesign -d --entitlements - "$$APP" 2>/dev/null | grep -q 'device.audio-input' \
		|| { echo "error: audio-input entitlement missing" >&2; exit 1; }; \
	echo "verify-release: OK ($$APP, $$(defaults read "$$PWD/$$APP/Contents/Info" CFBundleShortVersionString))"

clean:
	rm -rf build DeskPixie.xcodeproj
	cd Packages/DeskPixieCore && swift package clean
