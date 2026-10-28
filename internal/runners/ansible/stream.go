// Package ansible runs playbooks, ad hoc commands and inventory imports with
// ansible-runner. It implements ansible-runner's streaming protocol
// (transmit / worker / process), so the same byte stream drives a local
// process, an execution environment container or a remote node.
package ansible

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Mount adds a directory to the transmitted private data dir under Prefix
// (e.g. a shared project checkout as "project") without copying it first.
type Mount struct {
	Prefix string
	Path   string
}

// Transmit writes the stream ansible-runner worker expects on stdin:
// {"kwargs": ...}, {"zipfile": N} followed by the base64 zip of the private
// data directory, then {"eof": true}.
func Transmit(w io.Writer, kwargs map[string]any, privateDataDir string, mounts ...Mount) error {
	bw := bufio.NewWriter(w)
	b, err := json.Marshal(map[string]any{"kwargs": kwargs})
	if err != nil {
		return err
	}
	bw.Write(b)
	bw.WriteByte('\n')
	if privateDataDir != "" {
		var zbuf bytes.Buffer
		if err := zipDirs(&zbuf, append([]Mount{{Path: privateDataDir}}, mounts...)); err != nil {
			return err
		}
		fmt.Fprintf(bw, "{\"zipfile\": %d}\n", zbuf.Len())
		enc := base64.NewEncoder(base64.StdEncoding, bw)
		if _, err := enc.Write(zbuf.Bytes()); err != nil {
			return err
		}
		if err := enc.Close(); err != nil {
			return err
		}
	}
	bw.WriteString("{\"eof\": true}\n")
	return bw.Flush()
}

func zipDirs(w io.Writer, mounts []Mount) error {
	zw := zip.NewWriter(w)
	for _, m := range mounts {
		if err := zipDir(zw, m.Path, m.Prefix); err != nil {
			return err
		}
	}
	return zw.Close()
}

func zipDir(zw *zip.Writer, dir, prefix string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if d.Name() == ".git" && path != dir {
			// Project checkouts carry a .git file/dir that jobs do not need.
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == "." {
			if prefix == "" {
				return nil
			}
			rel = ""
		}
		rel = filepath.ToSlash(filepath.Join(prefix, rel))
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = rel
		if d.IsDir() {
			hdr.Name += "/"
			_, err := zw.CreateHeader(hdr)
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// Store symlinks as links (ansible-runner restores them).
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			hdr.Method = zip.Store
			fw, err := zw.CreateHeader(hdr)
			if err != nil {
				return err
			}
			_, err = fw.Write([]byte(target))
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		hdr.Method = zip.Deflate
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(fw, f)
		return err
	})
}

// StreamMessage is one decoded line of ansible-runner worker output.
type StreamMessage struct {
	// Status messages: {"status": "...", "runner_ident": ...}
	Status string
	RC     *int
	// Event is set for job events.
	Event map[string]any
}

// Process reads ansible-runner worker output, calling onMsg for statuses and
// events, and unpacks the returned artifacts zip into artifactsDir.
func Process(r io.Reader, artifactsDir string, onMsg func(StreamMessage)) error {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var data map[string]any
			if jerr := json.Unmarshal(line, &data); jerr != nil {
				// Non-JSON output (e.g. container runtime noise): surface it as verbose output.
				onMsg(StreamMessage{Event: map[string]any{"event": "verbose", "stdout": strings.TrimRight(string(line), "\r\n")}})
			} else if size, ok := data["zipfile"]; ok {
				n, _ := size.(float64)
				if err := unzipStream(br, int64(n), artifactsDir); err != nil {
					return fmt.Errorf("reading artifacts: %w", err)
				}
			} else if _, ok := data["eof"]; ok {
				return nil
			} else if st, ok := data["status"].(string); ok {
				msg := StreamMessage{Status: st}
				if rc, ok := data["rc"].(float64); ok {
					v := int(rc)
					msg.RC = &v
				}
				onMsg(msg)
			} else if ev, ok := data["event"]; ok && ev != "keepalive" {
				// Keepalives (AWX_RUNNER_KEEPALIVE_SECONDS) only keep the stream open.
				onMsg(StreamMessage{Event: data})
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func unzipStream(r io.Reader, size int64, dest string) error {
	// size is the decoded zip length; base64 text is 4 bytes per 3 decoded (padded).
	encLen := ((size + 2) / 3) * 4
	raw, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, io.LimitReader(r, encLen)))
	if err != nil {
		return err
	}
	if dest == "" {
		return nil
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		name := filepath.Clean(f.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue
		}
		target := filepath.Join(dest, name)
		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(target), 0o755)
		if f.Mode()&os.ModeSymlink != 0 {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode().Perm()|0o600)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
