// Package eecontents reads what an execution environment's image holds:
// its Ansible collections, ansible-core and Python versions and
// ansible.cfg. It reads the image's files from the registry
// (go-containerregistry), without running the image or needing a
// container runtime.
package eecontents

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Collection is an installed Ansible collection.
type Collection struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Path is the collections directory it is in.
	Path string `json:"path"`
}

// Contents is what an image holds.
type Contents struct {
	Image  string `json:"image"`
	Digest string `json:"digest"`
	// AnsibleCore is ansible-core's version; Ansible the community package's,
	// when installed.
	AnsibleCore string       `json:"ansible_core"`
	Ansible     string       `json:"ansible"`
	Python      string       `json:"python"`
	Collections []Collection `json:"collections"`
	// AnsibleCfg is the configuration file's text, from AnsibleCfgPath.
	AnsibleCfg     string `json:"ansible_cfg"`
	AnsibleCfgPath string `json:"ansible_cfg_path"`
	OS             string `json:"os"`
	Architecture   string `json:"architecture"`
	Created        string `json:"created"`
}

// Registry is how to reach the image's registry.
type Registry struct {
	Username, Password string
	// Insecure skips TLS verification (the credential's verify_ssl off).
	Insecure bool
}

func (r Registry) options(ctx context.Context) []remote.Option {
	opts := []remote.Option{remote.WithContext(ctx), remote.WithPlatform(v1.Platform{OS: "linux", Architecture: "amd64"})}
	if r.Username != "" || r.Password != "" {
		opts = append(opts, remote.WithAuth(&authn.Basic{Username: r.Username, Password: r.Password}))
	}
	if r.Insecure {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // the credential says so
		opts = append(opts, remote.WithTransport(t))
	}
	return opts
}

func image(ctx context.Context, ref string, reg Registry) (v1.Image, error) {
	r, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("image %q: %w", ref, err)
	}
	img, err := remote.Image(r, reg.options(ctx)...)
	if err != nil {
		return nil, fmt.Errorf("reading %s from its registry: %w", ref, err)
	}
	return img, nil
}

// Digest is the digest of the image ref names (for linux/amd64), read from
// its manifest only.
func Digest(ctx context.Context, ref string, reg Registry) (string, error) {
	img, err := image(ctx, ref, reg)
	if err != nil {
		return "", err
	}
	d, err := img.Digest()
	return d.String(), err
}

var (
	manifestRe = regexp.MustCompile(`^(.*?)/?ansible_collections/([^/]+)/([^/]+)/MANIFEST\.json$`)
	coreRe     = regexp.MustCompile(`(?:^|/)lib/python(3\.\d+)/site-packages/ansible_core-([^/]+)\.dist-info/METADATA$`)
	ansibleRe  = regexp.MustCompile(`(?:^|/)site-packages/ansible-(\d[^/]*)\.dist-info/METADATA$`)
)

// maxCfg bounds the configuration file read.
const maxCfg = 64 << 10

// Read reads the contents of the image ref names: its files, flattened over
// its layers.
func Read(ctx context.Context, ref string, reg Registry) (*Contents, error) {
	img, err := image(ctx, ref, reg)
	if err != nil {
		return nil, err
	}
	d, err := img.Digest()
	if err != nil {
		return nil, err
	}
	cf, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("reading %s's configuration: %w", ref, err)
	}
	c := &Contents{Image: ref, Digest: d.String(), OS: cf.OS, Architecture: cf.Architecture, Collections: []Collection{},
		AnsibleCfgPath: "/etc/ansible/ansible.cfg"}
	if !cf.Created.IsZero() {
		c.Created = cf.Created.UTC().Format("2006-01-02T15:04:05Z")
	}
	for _, e := range cf.Config.Env {
		if v, ok := strings.CutPrefix(e, "ANSIBLE_CONFIG="); ok && v != "" {
			c.AnsibleCfgPath = path.Clean("/" + v)
		}
	}
	rc := mutate.Extract(img)
	defer rc.Close()
	if err := c.scan(ctx, tar.NewReader(rc)); err != nil {
		return nil, fmt.Errorf("reading %s's files: %w", ref, err)
	}
	sort.Slice(c.Collections, func(i, j int) bool {
		if c.Collections[i].Name != c.Collections[j].Name {
			return c.Collections[i].Name < c.Collections[j].Name
		}
		return c.Collections[i].Path < c.Collections[j].Path
	})
	return c, nil
}

// scan reads the files of interest from the flattened image.
func (c *Contents) scan(ctx context.Context, tr *tar.Reader) error {
	cfg := strings.TrimPrefix(c.AnsibleCfgPath, "/")
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		p := strings.TrimPrefix(path.Clean("/"+h.Name), "/")
		switch {
		case p == cfg:
			b, err := io.ReadAll(io.LimitReader(tr, maxCfg))
			if err != nil {
				return err
			}
			c.AnsibleCfg = string(b)
		case manifestRe.MatchString(p):
			m := manifestRe.FindStringSubmatch(p)
			var man struct {
				CollectionInfo struct {
					Namespace, Name, Version string
				} `json:"collection_info"`
			}
			b, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return err
			}
			if json.NewDecoder(bytes.NewReader(b)).Decode(&man) != nil {
				continue
			}
			ns, n := man.CollectionInfo.Namespace, man.CollectionInfo.Name
			if ns == "" || n == "" {
				ns, n = m[2], m[3]
			}
			c.Collections = append(c.Collections, Collection{Name: ns + "." + n, Version: man.CollectionInfo.Version, Path: "/" + m[1]})
		case coreRe.MatchString(p):
			m := coreRe.FindStringSubmatch(p)
			c.Python, c.AnsibleCore = m[1], m[2]
		case ansibleRe.MatchString(p):
			c.Ansible = ansibleRe.FindStringSubmatch(p)[1]
		}
	}
}
