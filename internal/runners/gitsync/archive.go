package gitsync

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ulikunitz/xz"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/resources"
)

// SyncProject syncs a project of the given scm_type: git or archive.
func SyncProject(ctx context.Context, app *api.App, scmType string, src Source, logf func(string)) (string, string, error) {
	if scmType == "archive" {
		return SyncArchive(ctx, app, src, logf)
	}
	return Sync(ctx, app, src, logf)
}

// SyncArchive downloads an archive project's URL and unpacks it once per
// checksum, as AWX's project update does with get_url and its
// project_archive action: the revision is the archive's SHA-1, zip and tar
// (plain, gzip, bzip2 or xz) are told apart by content, and a single
// top-level directory is removed. The credential's username and password are
// sent with basic auth. Each checksum gets its own fresh directory, so
// scm_clean and scm_delete_on_update only unpack a known checksum again.
func SyncArchive(ctx context.Context, app *api.App, src Source, logf func(string)) (string, string, error) {
	lock := projectLock(src.ProjectID)
	lock.Lock()
	defer lock.Unlock()

	base := filepath.Join(app.Cfg.ProjectsRoot, ".revisions", fmt.Sprint(src.ProjectID))
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return "", "", err
	}
	if src.CredentialID != 0 {
		cred, err := resources.ResolveCredential(ctx, app, src.CredentialID)
		if err != nil {
			return "", "", err
		}
		user, _ := cred.Inputs["username"].(string)
		pass, _ := cred.Inputs["password"].(string)
		if user != "" || pass != "" {
			req.SetBasicAuth(user, pass)
		}
	}
	logf("Downloading " + redact(src.URL))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("downloading %s failed: %v", redact(src.URL), errors.Unwrap(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("downloading %s failed: HTTP %s", redact(src.URL), resp.Status)
	}
	f, err := os.CreateTemp(base, "download-*.tmp")
	if err != nil {
		return "", "", err
	}
	defer func() { f.Close(); os.Remove(f.Name()) }()
	h := sha1.New()
	size, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("downloading %s failed: %v", redact(src.URL), err)
	}
	sha := hex.EncodeToString(h.Sum(nil))
	logf(fmt.Sprintf("Revision %s (SHA-1 of %d bytes)", sha, size))
	dir := RevisionDir(app.Cfg.ProjectsRoot, src.ProjectID, sha)
	if _, err := os.Stat(dir); err == nil && !src.Clean && !src.DeleteOnUpdate {
		logf("Revision already unpacked on this node")
		return sha, dir, nil
	}
	tmp := dir + ".tmp"
	os.RemoveAll(tmp)
	n, err := unpack(f, size, tmp, logf)
	if err != nil {
		os.RemoveAll(tmp)
		return "", "", err
	}
	os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		return "", "", err
	}
	logf(fmt.Sprintf("Unpacked %d files", n))
	return sha, dir, nil
}

// entry is one member of a zip or tar archive.
type entry struct {
	name, link string
	dir        bool
	symlink    bool
	hardlink   bool
	exec       bool
	open       func() (io.ReadCloser, error)
}

var errNotArchive = errors.New("The downloaded file is not a zip or tar archive.")

// members calls fn for each member of the archive in f.
func members(f *os.File, size int64, fn func(entry) error) error {
	if z, err := zip.NewReader(f, size); err == nil {
		for _, m := range z.File {
			mode := m.Mode()
			e := entry{name: m.Name, dir: mode.IsDir() || strings.HasSuffix(m.Name, "/"), exec: mode&0o111 != 0, open: m.Open}
			if mode&os.ModeSymlink != 0 {
				r, err := m.Open()
				if err != nil {
					return err
				}
				target, err := io.ReadAll(io.LimitReader(r, 4096))
				r.Close()
				if err != nil {
					return err
				}
				e.symlink, e.link = true, string(target)
			}
			if err := fn(e); err != nil {
				return err
			}
		}
		return nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	br := bufio.NewReader(f)
	magic, _ := br.Peek(6)
	var r io.Reader = br
	switch {
	case bytes.HasPrefix(magic, []byte{0x1f, 0x8b}):
		gz, err := gzip.NewReader(br)
		if err != nil {
			return errNotArchive
		}
		r = gz
	case bytes.HasPrefix(magic, []byte("BZh")):
		r = bzip2.NewReader(br)
	case bytes.HasPrefix(magic, []byte{0xfd, '7', 'z', 'X', 'Z', 0}):
		x, err := xz.NewReader(br)
		if err != nil {
			return errNotArchive
		}
		r = x
	}
	tr := tar.NewReader(r)
	for i := 0; ; i++ {
		hdr, err := tr.Next()
		if err == io.EOF && i > 0 {
			return nil
		}
		if err != nil {
			if i == 0 {
				return errNotArchive
			}
			return fmt.Errorf("reading the archive: %v", err)
		}
		e := entry{name: hdr.Name, link: hdr.Linkname, exec: hdr.Mode&0o111 != 0,
			open: func() (io.ReadCloser, error) { return io.NopCloser(tr), nil }}
		switch hdr.Typeflag {
		case tar.TypeDir:
			e.dir = true
		case tar.TypeSymlink:
			e.symlink = true
		case tar.TypeLink:
			e.hardlink = true
		case tar.TypeReg, '\x00': // '\x00' is the old regular file flag
		default:
			continue
		}
		if err := fn(e); err != nil {
			return err
		}
	}
}

// unpack extracts the archive into dir and returns the number of files.
// Like AWX it removes a single top-level directory, found from the members'
// names (AWX also drops a single top-level file, which is kept here). Unlike
// AWX it refuses members outside the project, keeps links that stay inside
// it, and never writes through a link.
func unpack(f *os.File, size int64, dir string, logf func(string)) (int, error) {
	roots := map[string]bool{} // top-level name: whether it is a directory
	if err := members(f, size, func(e entry) error {
		top, rest, nested := strings.Cut(e.name, "/")
		roots[top] = roots[top] || nested && rest != "" || e.dir
		return nil
	}); err != nil {
		return 0, err
	}
	strip := ""
	if len(roots) == 1 {
		for r, isDir := range roots {
			if isDir && r != "" && r != ".." {
				strip = r + "/"
			}
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	local := func(name string) (string, bool) {
		name = strings.TrimPrefix(name, strip)
		if name == "" || name == "." || name == strings.TrimSuffix(strip, "/") {
			return "", true
		}
		name = strings.TrimSuffix(name, "/")
		return name, filepath.IsLocal(name)
	}
	files := 0
	err := members(f, size, func(e entry) error {
		name, ok := local(e.name)
		if !ok {
			return fmt.Errorf("The archive member %q is outside the project.", e.name)
		}
		if name == "" {
			return nil
		}
		if throughLink(dir, name) {
			logf(fmt.Sprintf("Skipping %s: it is inside a link", name))
			return nil
		}
		dest := filepath.Join(dir, filepath.FromSlash(name))
		if e.dir {
			return os.MkdirAll(dest, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		os.Remove(dest)
		switch {
		case e.symlink:
			if filepath.IsAbs(e.link) || !filepath.IsLocal(path.Join(path.Dir(name), e.link)) {
				logf(fmt.Sprintf("Skipping %s: its link leaves the project", name))
				return nil
			}
			return os.Symlink(e.link, dest)
		case e.hardlink:
			target, ok := local(e.link)
			if !ok || target == "" {
				logf(fmt.Sprintf("Skipping %s: its link leaves the project", name))
				return nil
			}
			if err := os.Link(filepath.Join(dir, filepath.FromSlash(target)), dest); err != nil {
				logf(fmt.Sprintf("Skipping %s: %v", name, err))
				return nil
			}
			files++
			return nil
		}
		perm := os.FileMode(0o644)
		if e.exec {
			perm = 0o755
		}
		r, err := e.open()
		if err != nil {
			return err
		}
		defer r.Close()
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, r); err != nil {
			out.Close()
			return fmt.Errorf("reading the archive: %v", err)
		}
		files++
		return out.Close()
	})
	return files, err
}

// throughLink reports whether a parent directory of name under dir is a
// symbolic link, so that links are only ever checked against real paths.
func throughLink(dir, name string) bool {
	p := dir
	parts := strings.Split(name, "/")
	for _, part := range parts[:len(parts)-1] {
		p = filepath.Join(p, part)
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}
