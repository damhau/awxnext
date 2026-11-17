package resources

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/api"
)

// A version's files against another version's, for its review: which were
// added, removed or changed (FILES.json's checksums), and the lines that
// changed in the text files.

const (
	diffMaxFile  = 256 << 10
	diffMaxTotal = 2 << 20
	diffMaxEdits = 2000
	diffContext  = 3
)

type versionFiles struct {
	id      int64
	version string
	files   map[string]string
	storage string
	place   string
}

func loadVersionFiles(c *Ctx, id int64) (*versionFiles, error) {
	v := &versionFiles{id: id, files: map[string]string{}}
	var fm struct {
		Files []struct {
			Name   string `json:"name"`
			Ftype  string `json:"ftype"`
			Sha256 string `json:"chksum_sha256"`
		} `json:"files"`
	}
	if err := c.DB().QueryRow(c, "SELECT version, files, storage, location FROM collection_versions WHERE id = $1", id).
		Scan(&v.version, &fm, &v.storage, &v.place); err != nil {
		return nil, err
	}
	for _, f := range fm.Files {
		if f.Ftype == "file" {
			v.files[f.Name] = f.Sha256
		}
	}
	return v, nil
}

// previousVersion is the highest version of the collection below version
// that the user sees.
func previousVersion(c *Ctx, coll, id int64, version string) (int64, error) {
	rows, err := c.DB().Query(c, "SELECT id, version FROM collection_versions WHERE collection_id = $1 AND id <> $2", coll, id)
	if err != nil {
		return 0, err
	}
	type cand struct {
		id      int64
		version string
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (cand, error) {
		var v cand
		err := row.Scan(&v.id, &v.version)
		return v, err
	})
	if err != nil {
		return 0, err
	}
	sort.Slice(all, func(i, j int) bool { return compareSemver(all[i].version, all[j].version) > 0 })
	for _, v := range all {
		if compareSemver(v.version, version) >= 0 {
			continue
		}
		if ok, _ := api.CanView(c, api.Lookup("collection_versions"), v.id); ok {
			return v.id, nil
		}
	}
	return 0, nil
}

// versionDiff compares a version with ?against=<id>, by default the
// previous version.
func versionDiff(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
	id, _ := api.ID(obj["id"])
	coll, _ := api.ID(obj["collection"])
	cur, err := loadVersionFiles(c, id)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	var other int64
	if s := r.URL.Query().Get("against"); s != "" {
		other, err = strconv.ParseInt(s, 10, 64)
		var otherColl int64
		if err == nil {
			err = c.DB().QueryRow(c, "SELECT collection_id FROM collection_versions WHERE id = $1", other).Scan(&otherColl)
		}
		ok := false
		if err == nil && otherColl == coll {
			ok, _ = api.CanView(c, api.Lookup("collection_versions"), other)
		}
		if !ok {
			api.WriteError(w, api.BadRequest("against: no version of the same collection with this id."))
			return
		}
	} else if other, err = previousVersion(c, coll, id, cur.version); err != nil {
		api.WriteError(w, err)
		return
	}
	out := Obj{"version": Obj{"id": id, "version": cur.version}, "against": nil}
	old := &versionFiles{files: map[string]string{}}
	if other != 0 {
		if old, err = loadVersionFiles(c, other); err != nil {
			api.WriteError(w, err)
			return
		}
		out["against"] = Obj{"id": other, "version": old.version}
	}
	type change struct{ name, status string }
	var changes []change
	for name, sum := range cur.files {
		switch prev, ok := old.files[name]; {
		case !ok:
			changes = append(changes, change{name, "added"})
		case prev != sum:
			changes = append(changes, change{name, "changed"})
		}
	}
	for name := range old.files {
		if _, ok := cur.files[name]; !ok {
			changes = append(changes, change{name, "removed"})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].name < changes[j].name })
	var curText, oldText map[string][]byte
	if len(changes) > 0 {
		want := map[string]bool{}
		for _, ch := range changes {
			want[ch.name] = true
		}
		if curText, err = versionMembers(c, cur, want); err == nil && other != 0 {
			oldText, err = versionMembers(c, old, want)
		}
		if err != nil {
			api.WriteError(w, err)
			return
		}
	}
	files := make([]Obj, 0, len(changes))
	total := 0
	counts := map[string]int{"added": 0, "removed": 0, "changed": 0}
	for _, ch := range changes {
		counts[ch.status]++
		f := Obj{"name": ch.name, "status": ch.status}
		a, aok := oldText[ch.name]
		b, bok := curText[ch.name]
		switch {
		case (ch.status != "added" && !aok) || (ch.status != "removed" && !bok):
			f["omitted"] = "too_large"
		case !isText(a) || !isText(b):
			f["omitted"] = "binary"
		default:
			d, ok := unifiedDiff(splitLines(string(a)), splitLines(string(b)))
			switch {
			case !ok:
				f["omitted"] = "too_large"
			case total+len(d) > diffMaxTotal:
				f["omitted"] = "total"
			default:
				total += len(d)
				f["diff"] = d
			}
		}
		files = append(files, f)
	}
	out["files"] = files
	out["counts"] = counts
	api.WriteJSON(w, 200, out)
}

// versionMembers reads the wanted files of a version's artifact, those
// small enough to compare.
func versionMembers(c *Ctx, v *versionFiles, want map[string]bool) (map[string][]byte, error) {
	data, err := loadArtifact(c, c.App, c.DB(), v.id, v.storage, v.place)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		p, ok := memberPath(h.Name)
		if !ok || !want[p] || h.Typeflag != tar.TypeReg || h.Size > diffMaxFile {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, diffMaxFile))
		if err != nil {
			return nil, err
		}
		out[p] = b
	}
}

func isText(b []byte) bool {
	return utf8.Valid(b) && bytes.IndexByte(b, 0) < 0
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type diffOp struct {
	kind byte
	a, b int
}

// lineDiff is Myers' diff of a and b after their common prefix and suffix,
// or false when they differ by more than diffMaxEdits lines.
func lineDiff(a, b []string) ([]diffOp, bool) {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	mid, ok := myers(a[pre:len(a)-suf], b[pre:len(b)-suf])
	if !ok {
		return nil, false
	}
	ops := make([]diffOp, 0, pre+len(mid)+suf)
	for i := 0; i < pre; i++ {
		ops = append(ops, diffOp{' ', i, i})
	}
	for _, op := range mid {
		ops = append(ops, diffOp{op.kind, op.a + pre, op.b + pre})
	}
	for i := 0; i < suf; i++ {
		ops = append(ops, diffOp{' ', len(a) - suf + i, len(b) - suf + i})
	}
	return ops, true
}

func myers(a, b []string) ([]diffOp, bool) {
	n, m := len(a), len(b)
	if n+m == 0 {
		return nil, true
	}
	off := n + m + 1
	v := make([]int, 2*off+1)
	var trace [][]int
	for d := 0; d <= n+m; d++ {
		if d > diffMaxEdits {
			return nil, false
		}
		trace = append(trace, append([]int(nil), v[off-d-1:off+d+2]...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				return backtrack(trace, n, m), true
			}
		}
	}
	return nil, false
}

func backtrack(trace [][]int, n, m int) []diffOp {
	var ops []diffOp
	x, y := n, m
	for d := len(trace) - 1; d > 0; d-- {
		prev := trace[d]
		at := func(k int) int { return prev[k+d+1] }
		k := x - y
		var pk int
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			pk = k + 1
		} else {
			pk = k - 1
		}
		px := at(pk)
		py := px - pk
		for x > px && y > py {
			x--
			y--
			ops = append(ops, diffOp{' ', x, y})
		}
		if x == px {
			y--
			ops = append(ops, diffOp{'+', x, y})
		} else {
			x--
			ops = append(ops, diffOp{'-', x, y})
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		ops = append(ops, diffOp{' ', x, y})
	}
	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return ops
}

// unifiedDiff is a diff -u of a and b's lines, without the file header.
func unifiedDiff(a, b []string) (string, bool) {
	ops, ok := lineDiff(a, b)
	if !ok {
		return "", false
	}
	var sb strings.Builder
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start := max(i-diffContext, 0)
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*diffContext {
				end = min(end+diffContext, len(ops))
				break
			}
			end = run
		}
		aStart, bStart, aLen, bLen := ops[start].a, ops[start].b, 0, 0
		for _, op := range ops[start:end] {
			if op.kind != '+' {
				aLen++
			}
			if op.kind != '-' {
				bLen++
			}
		}
		fmt.Fprintf(&sb, "@@ -%s +%s @@\n", hunkRange(aStart, aLen), hunkRange(bStart, bLen))
		for _, op := range ops[start:end] {
			line := ""
			switch op.kind {
			case '+':
				line = b[op.b]
			default:
				line = a[op.a]
			}
			sb.WriteByte(op.kind)
			sb.WriteString(line)
			if !strings.HasSuffix(line, "\n") {
				sb.WriteString("\n\\ No newline at end of file\n")
			}
		}
		i = end
	}
	return sb.String(), true
}

func hunkRange(start, n int) string {
	if n == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	if n == 1 {
		return strconv.Itoa(start + 1)
	}
	return fmt.Sprintf("%d,%d", start+1, n)
}
