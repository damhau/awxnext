package test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/damhau/awxnext/internal/config"
	"github.com/damhau/awxnext/internal/docsgen"
)

// docs/configuration.md is generated: it must match the code.
func TestConfigurationReferenceCurrent(t *testing.T) {
	b, err := os.ReadFile("../docs/configuration.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != docsgen.Configuration() {
		t.Fatal("docs/configuration.md is out of date: run go run ./cmd/gendocs")
	}
}

// Every environment variable the code reads is in config.Vars, and every
// one there is read somewhere.
func TestConfigurationVarsComplete(t *testing.T) {
	// Read by the process, not configuration; the tests' and awxbench's own.
	system := map[string]bool{"HOME": true, "PATH": true, "KUBERNETES_SERVICE_HOST": true, "KUBERNETES_SERVICE_PORT": true,
		"AZURE_FEDERATED_TOKEN_FILE": true, "AWXNEXT_TEST_DATABASE_URL": true, "AWXBENCH_PASSWORD": true}
	named := regexp.MustCompile(`"(AWXNEXT_[A-Z0-9_]+)"`)
	read := regexp.MustCompile(`(?:Getenv|LookupEnv|setting)\("([A-Z][A-Z0-9_]+)"`)
	kms := regexp.MustCompile(`get\("([A-Z][A-Z0-9_]+)"\)`)
	used := map[string]bool{}
	var sources strings.Builder
	err := filepath.Walk("..", func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if n := info.Name(); n == "node_modules" || n == ".git" || n == "test" || n == "ui" || n == "docs" {
				return filepath.SkipDir
			}
			return nil
		}
		if p == filepath.Join("..", "internal", "config", "vars.go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		goFile := strings.HasSuffix(p, ".go")
		if !goFile && !strings.HasPrefix(p, filepath.Join("..", "deploy")) && filepath.Base(p) != "Dockerfile" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sources.Write(b)
		if !goFile {
			return nil
		}
		for _, m := range named.FindAllStringSubmatch(string(b), -1) {
			if !system[m[1]] {
				used[m[1]] = true
			}
		}
		for _, m := range read.FindAllStringSubmatch(string(b), -1) {
			if !system[m[1]] {
				used[m[1]] = true
			}
		}
		if strings.HasSuffix(p, filepath.Join("kms", "kms.go")) {
			for _, m := range kms.FindAllStringSubmatch(string(b), -1) {
				used["AWXNEXT_"+m[1]] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, v := range config.Vars {
		listed[v.Name] = true
		if !strings.Contains(sources.String(), v.Name) && !used[v.Name] {
			t.Errorf("%s is in config.Vars but nothing reads it", v.Name)
		}
	}
	for name := range used {
		if !listed[name] {
			t.Errorf("%s is read but not in config.Vars (the configuration reference)", name)
		}
	}
}
