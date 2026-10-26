// Package projectscan finds playbooks and inventory files in a project checkout.
package projectscan

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Files are the playbooks and potential inventory sources of a project.
type Files struct {
	Playbooks   []string `json:"playbooks"`
	Inventories []string `json:"inventories"`
}

// Directories that never contain playbooks or inventories of their own.
var skipDirs = map[string]bool{
	".git": true, ".github": true, ".gitlab": true, "roles": true, "collections": true, "tasks": true,
	"handlers": true, "vars": true, "defaults": true, "meta": true, "templates": true, "files": true,
	"group_vars": true, "host_vars": true, "molecule": true, "node_modules": true, "venv": true, ".venv": true,
	"filter_plugins": true, "library": true, "module_utils": true, "plugins": true, "tests": false,
}

const maxFileSize = 1 << 20

// Scan walks dir and classifies files like AWX: a playbook is a YAML file whose
// top level is a list containing a play (hosts or import_playbook); anything
// else that looks like an inventory (INI, YAML, JSON, scripts) is offered as
// an inventory file.
func Scan(dir string) (Files, error) {
	files := Files{Playbooks: []string{}, Inventories: []string{}}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != dir && (skipDirs[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(name, ".") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxFileSize || !info.Mode().IsRegular() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		switch ext {
		case ".yml", ".yaml":
			kind := classifyYAML(path)
			if kind == "playbook" {
				files.Playbooks = append(files.Playbooks, rel)
			} else if kind == "inventory" {
				files.Inventories = append(files.Inventories, rel)
			}
		case ".ini", ".json", ".cfg":
			if name != "ansible.cfg" && name != "package.json" && name != "tsconfig.json" {
				files.Inventories = append(files.Inventories, rel)
			}
		case "":
			if looksLikeInventory(path) {
				files.Inventories = append(files.Inventories, rel)
			}
		}
		return nil
	})
	sort.Strings(files.Playbooks)
	sort.Strings(files.Inventories)
	return files, err
}

func classifyYAML(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return ""
	}
	switch x := doc.(type) {
	case []any:
		for _, item := range x {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			for _, k := range []string{"hosts", "import_playbook", "ansible.builtin.import_playbook", "include"} {
				if _, ok := m[k]; ok {
					return "playbook"
				}
			}
		}
	case map[string]any:
		// YAML inventories have groups at the top level (all:, children:, hosts:) or an inventory plugin.
		if _, ok := x["plugin"]; ok {
			return "inventory"
		}
		for _, v := range x {
			if g, ok := v.(map[string]any); ok {
				if _, ok := g["hosts"]; ok {
					return "inventory"
				}
				if _, ok := g["children"]; ok {
					return "inventory"
				}
			}
		}
	}
	return ""
}

// looksLikeInventory accepts extensionless INI-style inventories and executable scripts.
func looksLikeInventory(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.Mode()&0o111 != 0 {
		return true
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	s := string(b)
	return strings.Contains(s, "[") && strings.Contains(s, "]") || strings.Contains(s, "ansible_host")
}
