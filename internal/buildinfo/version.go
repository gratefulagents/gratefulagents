// Package buildinfo exposes release metadata stamped into the server binary.
package buildinfo

// Version is replaced at build time for release images. Development binaries
// keep "dev" so local builds are easy to identify and do not pretend to be a
// published app release.
var Version = "dev"
