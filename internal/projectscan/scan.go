// Package projectscan finds playbooks and inventory files in a project checkout.
package projectscan

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
// an inventory file. Without followLinks only links to files inside dir are
// listed, so a project cannot make the scan read files elsewhere on the
// server. With followLinks (AWX_SHOW_PLAYBOOK_LINKS, a superuser's choice)
// links are followed wherever they point, as in AWX. Real directories are
// walked before links, so a directory reached both ways is listed under its
// real path, and each directory is walked once.
func Scan(dir string, followLinks bool) (Files, error) {
	files := Files{Playbooks: []string{}, Inventories: []string{}}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return files, nil
	}
	inside := func(p string) bool {
		rel, err := filepath.Rel(root, p)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
	}
	seen := map[string]bool{root: true}
	var walk func(real, rel string)
	walk = func(real, rel string) {
		entries, err := os.ReadDir(real)
		if err != nil {
			return
		}
		// Two passes over the entries: real files and directories first, then links.
		for pass, d := range append(slices.Clone(entries), entries...) {
			name := d.Name()
			link := d.Type()&fs.ModeSymlink != 0
			if strings.HasPrefix(name, ".") || link != (pass >= len(entries)) {
				continue
			}
			path, relPath := filepath.Join(real, name), name
			if rel != "" {
				relPath = rel + "/" + name
			}
			info, err := d.Info()
			if err != nil {
				continue
			}
			if link {
				target, err := filepath.EvalSymlinks(path)
				if err != nil || (!followLinks && !inside(target)) {
					continue
				}
				if info, err = os.Stat(target); err != nil || (info.IsDir() && !followLinks) {
					continue
				}
				path = target
			}
			if info.IsDir() {
				if skipDirs[name] || seen[path] {
					continue
				}
				seen[path] = true
				walk(path, relPath)
				continue
			}
			if info.Size() > maxFileSize || !info.Mode().IsRegular() {
				continue
			}
			switch strings.ToLower(filepath.Ext(name)) {
			case ".yml", ".yaml":
				switch classifyYAML(path) {
				case "playbook":
					files.Playbooks = append(files.Playbooks, relPath)
				case "inventory":
					files.Inventories = append(files.Inventories, relPath)
				}
			case ".ini", ".json", ".cfg":
				if name != "ansible.cfg" && name != "package.json" && name != "tsconfig.json" {
					files.Inventories = append(files.Inventories, relPath)
				}
			case "":
				if looksLikeInventory(path) {
					files.Inventories = append(files.Inventories, relPath)
				}
			}
		}
	}
	walk(root, "")
	sort.Strings(files.Playbooks)
	sort.Strings(files.Inventories)
	return files, nil
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
