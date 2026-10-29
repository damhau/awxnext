package settings

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every setting must do something: read by the server ("KEY" in Go code) or
// the web UI (KEY in its TypeScript), or be marked CompatOnly with a help text
// saying it has no effect. A CompatOnly setting that code reads must lose
// the mark.
func TestEverySettingHasAnEffect(t *testing.T) {
	root := filepath.Join("..", "..")
	var goSrc, uiSrc strings.Builder
	read := func(dir string, into *strings.Builder, keep func(string) bool) {
		filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !keep(path) {
				return nil
			}
			b, err := os.ReadFile(path)
			if err == nil {
				into.Write(b)
				into.WriteByte('\n')
			}
			return nil
		})
	}
	goFile := func(p string) bool {
		return strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") && !strings.HasSuffix(p, filepath.Join("settings", "defs.go")) &&
			!strings.HasSuffix(p, filepath.Join("settings", "ldap.go"))
	}
	uiFile := func(p string) bool {
		return (strings.HasSuffix(p, ".ts") || strings.HasSuffix(p, ".tsx")) && !strings.Contains(p, ".test.")
	}
	read("internal", &goSrc, goFile)
	read("cmd", &goSrc, goFile)
	read(filepath.Join("ui", "src"), &uiSrc, uiFile)
	if goSrc.Len() == 0 || uiSrc.Len() == 0 {
		t.Fatal("source tree not found")
	}

	for _, d := range InCategory("all") {
		used := strings.Contains(goSrc.String(), `"`+d.Key+`"`) || regexp.MustCompile(`\b`+d.Key+`\b`).MatchString(uiSrc.String())
		// The six LDAP servers' settings are read as prefix + "SUFFIX".
		if m := regexp.MustCompile(`^AUTH_LDAP_(?:[1-5]_)?(.+)$`).FindStringSubmatch(d.Key); m != nil {
			used = used || strings.Contains(goSrc.String(), `"`+m[1]+`"`)
		}
		switch {
		case d.CompatOnly && used:
			t.Errorf("%s is marked CompatOnly but code reads it: remove the mark and its \"No effect\" help", d.Key)
		case d.CompatOnly && !strings.HasPrefix(d.Help, "No effect in awxnext"):
			t.Errorf("%s is CompatOnly: its help must start with \"No effect in awxnext\" and say why", d.Key)
		case !d.CompatOnly && !used:
			t.Errorf("%s is stored and shown, but nothing reads it: make it work, or mark it CompatOnly", d.Key)
		}
	}
}
