// Command gendocs writes docs/configuration.md and the settings list the
// UI's layout test reads, ui/src/lib/settings.generated.json (run from the
// repository's root): go run ./cmd/gendocs
package main

import (
	"fmt"
	"os"

	"github.com/damhau/awxnext/internal/docsgen"
)

func main() {
	for path, content := range map[string]string{
		"docs/configuration.md":              docsgen.Configuration(),
		"ui/src/lib/settings.generated.json": docsgen.SettingsList(),
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
