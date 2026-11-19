// Package hubdocs is the script that reads a collection's module and plugin
// docs with ansible-doc. The server runs it in a container or as a job user,
// and the shim of a docs pod runs it in the pod.
package hubdocs

import (
	_ "embed"
	"regexp"
)

//go:embed docs.py
var script string

// pick runs the script with ansible-doc's Python, which has Ansible's
// libraries, else python3.
const pick = `d=$(command -v ansible-doc) || exit 3
IFS= read -r p < "$d"
p=${p#\#!}
case "$p" in *python*) ;; *) p= ;; esac
exec ${p:-python3} -I -c "$0" "$d" "$1"`

// The script's exit codes, besides 0.
const (
	NoAnsibleDoc = 3
	BadArtifact  = 4
	Failed       = 5
)

var collectionName = regexp.MustCompile(`^[a-z0-9_]+\.[a-z0-9_]+$`)

// ValidName says whether fqcn is a collection's namespace.name.
func ValidName(fqcn string) bool {
	return collectionName.MatchString(fqcn)
}

// Argv runs the script on the collection fqcn, whose artifact it reads on
// stdin.
func Argv(fqcn string) []string {
	return []string{"sh", "-c", pick, script, fqcn}
}
