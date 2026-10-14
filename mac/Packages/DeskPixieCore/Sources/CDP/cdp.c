// CDP has no implementation of its own: dp_* symbols come from libdeskpixie.dylib
// at link time (the Xcode app target supplies -ldeskpixie). This file exists only
// because SwiftPM needs something to compile, otherwise no CDP.o is produced and
// the library link step fails. Do NOT define dp_* stubs here (see CDPStubs).
#include "libdeskpixie_shim.h"
