// Package conformance generates the language-neutral conformance suite in
// testdata/conformance: small synthetic databases and, for each, a JSON
// document of what the Go reader answers for a chosen list of addresses.
// A reader in another language proves it reads the format as this one does
// by matching every answer; testdata/conformance/README.md specifies the
// JSON documents.
//
// The databases are built with internal/build from synthetic sources
// written out in this package (made-up names, codes and numbers; no
// real-world data), and the GEO1 file is encoded here. Generation is
// deterministic: the build time and the builder string are fixed, so
// running it again gives byte-identical files. The root package's
// conformance test checks both the answers and that the committed files
// match a fresh generation.
//
// Regenerate the files with
//
//	go generate ./internal/conformance
//
// or, from the module root, go run ./internal/conformance/gen.
package conformance

//go:generate go run ./gen -dir ../../testdata/conformance
