.PHONY: build-dylib test clean
build-dylib:
	go build -buildmode=c-shared -o build/libdeskpixie.dylib ./cmd/libdeskpixie
	install_name_tool -id "@rpath/libdeskpixie.dylib" build/libdeskpixie.dylib
test:
	go test ./...
clean:
	rm -rf build
