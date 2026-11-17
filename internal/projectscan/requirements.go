package projectscan

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// RequirementFiles lists the requirement files of a checkout as AWX reads
// them: roles/requirements.yml, collections/requirements.yml, and a
// top-level requirements.yml holding roles, collections or both.
func RequirementFiles(dir string) (roles, collections []string) {
	first := func(names ...string) string {
		for _, n := range names {
			if st, err := os.Stat(filepath.Join(dir, n)); err == nil && st.Mode().IsRegular() {
				return n
			}
		}
		return ""
	}
	if f := first("roles/requirements.yml", "roles/requirements.yaml"); f != "" {
		roles = append(roles, f)
	}
	if f := first("collections/requirements.yml", "collections/requirements.yaml"); f != "" {
		collections = append(collections, f)
	}
	if f := first("requirements.yml", "requirements.yaml"); f != "" {
		b, _ := os.ReadFile(filepath.Join(dir, f))
		var doc any
		if yaml.Unmarshal(b, &doc) == nil {
			switch d := doc.(type) {
			case []any:
				roles = append(roles, f)
			case map[string]any:
				if _, ok := d["roles"]; ok {
					roles = append(roles, f)
				}
				if _, ok := d["collections"]; ok {
					collections = append(collections, f)
				}
			}
		}
	}
	return
}

// CollectionRequirement is a collection a requirement file names, to be
// installed from a Galaxy server.
type CollectionRequirement struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Source  string `json:"source,omitempty"`
	File    string `json:"file"`
}

var collectionNameRe = regexp.MustCompile(`^[A-Za-z0-9_]+\.[A-Za-z0-9_]+$`)

// CollectionRequirements reads the collections the requirement files of a
// checkout name, as ansible-galaxy does: a name with an optional
// version after a colon, or a mapping. Collections from git, a URL or a
// file are left out: no Galaxy server has them. Files outside the checkout
// are not read.
func CollectionRequirements(dir string) []CollectionRequirement {
	out := []CollectionRequirement{}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return out
	}
	_, files := RequirementFiles(dir)
	for _, f := range files {
		real, err := filepath.EvalSymlinks(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(root, real); err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		fh, err := os.Open(real)
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(fh, maxFileSize))
		fh.Close()
		if err != nil {
			continue
		}
		var doc struct {
			Collections []yaml.Node `yaml:"collections"`
		}
		if yaml.Unmarshal(b, &doc) != nil {
			continue
		}
		for _, node := range doc.Collections {
			r := CollectionRequirement{File: f}
			switch node.Kind {
			case yaml.ScalarNode:
				r.Name, r.Version, _ = strings.Cut(strings.TrimSpace(node.Value), ":")
			case yaml.MappingNode:
				var m struct {
					Name    string `yaml:"name"`
					Version string `yaml:"version"`
					Source  string `yaml:"source"`
					Type    string `yaml:"type"`
				}
				if node.Decode(&m) != nil || (m.Type != "" && m.Type != "galaxy") {
					continue
				}
				r.Name, r.Version, r.Source = m.Name, m.Version, m.Source
			}
			r.Name, r.Version = strings.TrimSpace(r.Name), strings.TrimSpace(r.Version)
			if collectionNameRe.MatchString(r.Name) {
				out = append(out, r)
			}
		}
	}
	return out
}
