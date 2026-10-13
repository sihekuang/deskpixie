.PHONY: project build test run clean

SWIFT_SOURCES := $(shell find DeskPixie -name '*.swift' -type f 2>/dev/null)

# Regenerate only when project.yml, the xcconfig or the set of app sources changes.
DeskPixie.xcodeproj/project.pbxproj: project.yml Configs/SharedSettings.xcconfig $(SWIFT_SOURCES)
	xcodegen generate

project: DeskPixie.xcodeproj/project.pbxproj

build: project
	xcodebuild -project DeskPixie.xcodeproj -scheme DeskPixie -configuration Debug -derivedDataPath build build

test:
	cd Packages/DeskPixieCore && swift test

run:
	open build/Build/Products/Debug/DeskPixie.app

clean:
	rm -rf build DeskPixie.xcodeproj
	cd Packages/DeskPixieCore && swift package clean
