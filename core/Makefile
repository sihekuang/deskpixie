.PHONY: build-dylib test clean
# Match the app's deployment target, or ld warns "dylib built for newer version".
MACOS_MIN := 14.0
build-dylib:
	MACOSX_DEPLOYMENT_TARGET=$(MACOS_MIN) CGO_CFLAGS="-mmacosx-version-min=$(MACOS_MIN)" CGO_LDFLAGS="-mmacosx-version-min=$(MACOS_MIN)" go build -buildmode=c-shared -o build/libdeskpixie.dylib ./cmd/libdeskpixie
	install_name_tool -id "@rpath/libdeskpixie.dylib" build/libdeskpixie.dylib
test:
	go test ./...
clean:
	rm -rf build
