// Command gen writes the conformance suite (package
// internal/conformance) to a directory, testdata/conformance by default.
// Run it from the module root, or through go generate ./internal/conformance.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/haxqer/ip2geo/internal/conformance"
)

func main() {
	dir := flag.String("dir", filepath.Join("testdata", "conformance"), "output directory")
	flag.Parse()
	if err := run(*dir); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(dir string) error {
	files, err := conformance.Generate()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range files {
		p := filepath.Join(dir, f.Name)
		if err := os.WriteFile(p, f.Data, 0o644); err != nil {
			return err
		}
		fmt.Printf("%s: %d bytes\n", p, len(f.Data))
	}
	return nil
}
