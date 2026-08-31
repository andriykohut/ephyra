package buildinfo

// version is overridden at build time with
//
//	-ldflags "-X github.com/andriykohut/ephyra/internal/buildinfo.version=<v>"
var version = "dev"

// Version returns the build version string. It's "dev" unless the linker set it.
func Version() string { return version }
