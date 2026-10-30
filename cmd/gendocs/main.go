// Command gendocs writes docs/configuration.md (run from the repository's
// root): go run ./cmd/gendocs
package main

import (
	"fmt"
	"os"

	"github.com/damhau/awxnext/internal/docsgen"
)

func main() {
	if err := os.WriteFile("docs/configuration.md", []byte(docsgen.Configuration()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
