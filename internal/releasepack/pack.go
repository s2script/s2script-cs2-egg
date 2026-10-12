// SPDX-License-Identifier: GPL-3.0-or-later
// Package releasepack creates a release only from a clean, exactly tagged Git tree.
package releasepack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/panel"
	"github.com/s2script/s2script-cs2-egg/internal/releasecontract"
)

const Toolchain = "go1.27.2"

type Options struct{ Root, Tag, Output, Go string }
type entry struct {
	Name string
	Data []byte
	Mode int64
}

func command(ctx context.Context, dir, program string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, program, args...)
	c.Dir = dir
	b, e := c.Output()
	if e != nil {
		return nil, fmt.Errorf("%s %v failed: %w", program, args, e)
	}
	return b, nil
}
func gitText(ctx context.Context, root string, args ...string) (string, error) {
	b, e := command(ctx, root, "git", args...)
	return strings.TrimSpace(string(b)), e
}
func Identity(ctx context.Context, root, tag string) (string, time.Time, error) {
	if !strings.HasPrefix(tag, "v") {
		return "", time.Time{}, errors.New("canonical v-prefixed release tag required")
	}
	if _, e := releasecontract.ParseVersion(strings.TrimPrefix(tag, "v")); e != nil {
		return "", time.Time{}, e
	}
	status, e := gitText(ctx, root, "status", "--porcelain=v1", "--untracked-files=all")
	if e != nil {
		return "", time.Time{}, e
	}
	if status != "" {
		return "", time.Time{}, errors.New("release source must be clean, including untracked files")
	}
	head, e := gitText(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if e != nil {
		return "", time.Time{}, e
	}
	target, e := gitText(ctx, root, "rev-parse", "--verify", "refs/tags/"+tag+"^{commit}")
	if e != nil || head != target || len(head) != 40 {
		return "", time.Time{}, errors.New("release tag must dereference to checked-out HEAD")
	}
	timestamp, e := gitText(ctx, root, "show", "-s", "--format=%ct", head)
	if e != nil {
		return "", time.Time{}, e
	}
	n, e := strconv.ParseInt(timestamp, 10, 64)
	return head, time.Unix(n, 0).UTC(), e
}
func tree(ctx context.Context, root, revision string) ([]entry, error) {
	b, e := command(ctx, root, "git", "ls-tree", "-rz", "--full-tree", revision)
	if e != nil {
		return nil, e
	}
	var entries []entry
	for _, line := range bytes.Split(b, []byte{0}) {
		if len(line) == 0 {
			continue
		}
		meta, name, ok := strings.Cut(string(line), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") || !filepath.IsLocal(name) || strings.ContainsAny(name, "\r\n\\") {
			return nil, errors.New("unsupported release Git tree entry")
		}
		data, e := command(ctx, root, "git", "cat-file", "blob", fields[2])
		if e != nil {
			return nil, e
		}
		if len(data) > 100<<20 {
			return nil, errors.New("release source file too large")
		}
		mode := int64(0644)
		if fields[0] == "100755" {
			mode = 0755
		}
		entries = append(entries, entry{name, data, mode})
		if len(entries) > 10000 {
			return nil, errors.New("release tree too large")
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}
func archive(path, prefix string, files []entry, at time.Time) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if e != nil {
		return e
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, v := range files {
		h := &tar.Header{Name: prefix + v.Name, Mode: v.Mode, Size: int64(len(v.Data)), ModTime: at, Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if e = tw.WriteHeader(h); e == nil {
			_, e = tw.Write(v.Data)
		}
		if e != nil {
			tw.Close()
			gz.Close()
			f.Close()
			return e
		}
	}
	if e = tw.Close(); e == nil {
		e = gz.Close()
	} else {
		gz.Close()
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	return e
}
func Digest(path string) (releasecontract.FileDigest, error) {
	f, e := os.Open(path)
	if e != nil {
		return releasecontract.FileDigest{}, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	return releasecontract.FileDigest{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, e
}
func VerifyFile(path string, want releasecontract.FileDigest) error {
	got, e := Digest(path)
	if e != nil {
		return e
	}
	if got != want {
		return errors.New("release asset size or digest mismatch")
	}
	return nil
}
func Build(ctx context.Context, o Options) (releasecontract.Manifest, error) {
	var zero releasecontract.Manifest
	root, e := filepath.Abs(o.Root)
	if e != nil {
		return zero, e
	}
	output, e := filepath.Abs(o.Output)
	if e != nil {
		return zero, e
	}
	if output == root || strings.HasPrefix(output, root+string(os.PathSeparator)) {
		return zero, errors.New("release output must be outside source checkout")
	}
	if _, e = os.Lstat(output); !os.IsNotExist(e) {
		return zero, errors.New("release output already exists or is inaccessible")
	}
	revision, at, e := Identity(ctx, root, o.Tag)
	if e != nil {
		return zero, e
	}
	if o.Go == "" {
		o.Go = "go"
	}
	goPath, e := exec.LookPath(o.Go)
	if e != nil {
		return zero, e
	}
	version, e := command(ctx, root, goPath, "env", "GOVERSION")
	if e != nil || strings.TrimSpace(string(version)) != Toolchain {
		return zero, errors.New("release requires pinned Go 1.27.2")
	}
	entries, e := tree(ctx, root, revision)
	if e != nil {
		return zero, e
	}
	source, e := os.MkdirTemp("", "s2release-source-")
	if e != nil {
		return zero, e
	}
	defer os.RemoveAll(source)
	for _, v := range entries {
		p := filepath.Join(source, v.Name)
		if e = os.MkdirAll(filepath.Dir(p), 0755); e == nil {
			e = os.WriteFile(p, v.Data, os.FileMode(v.Mode))
		}
		if e != nil {
			return zero, e
		}
	}
	if e = os.MkdirAll(filepath.Dir(output), 0755); e != nil {
		return zero, e
	}
	stage, e := os.MkdirTemp(filepath.Dir(output), ".s2release-")
	if e != nil {
		return zero, e
	}
	defer os.RemoveAll(stage)
	names := releasecontract.Names(strings.TrimPrefix(o.Tag, "v"))
	if e = archive(filepath.Join(stage, names["source"]), "s2script-cs2-egg-"+o.Tag+"/", entries, at); e != nil {
		return zero, e
	}
	deployment := []entry{}
	required := map[string]bool{"LICENSE": false, "NOTICE": false, "third_party/Go-LICENSE": false, "deploy/s2node.service": false, "deploy/s2node-launcher.conf": false}
	for _, v := range entries {
		if _, ok := required[v.Name]; ok {
			required[v.Name] = true
		}
		if strings.HasPrefix(v.Name, "deploy/") || strings.HasPrefix(v.Name, "examples/") || strings.HasPrefix(v.Name, "schemas/") || v.Name == "LICENSE" || v.Name == "NOTICE" || v.Name == "third_party/Go-LICENSE" {
			deployment = append(deployment, v)
		}
	}
	for name, ok := range required {
		if !ok {
			return zero, fmt.Errorf("missing deployment asset %s", name)
		}
	}
	if e = archive(filepath.Join(stage, names["deployment"]), "s2script-cs2-egg-"+o.Tag+"/", deployment, at); e != nil {
		return zero, e
	}
	egg, e := panel.Generate("ghcr.io/" + releasecontract.Repository + ":" + o.Tag)
	if e != nil {
		return zero, e
	}
	if e = os.WriteFile(filepath.Join(stage, names["egg"]), egg, 0644); e != nil {
		return zero, e
	}
	for _, component := range []string{"s2egg", "s2node"} {
		flags := "-s -w -buildid= -X github.com/s2script/s2script-cs2-egg/internal/buildinfo.Version=" + strings.TrimPrefix(o.Tag, "v") + " -X github.com/s2script/s2script-cs2-egg/internal/buildinfo.Revision=" + revision
		c := exec.CommandContext(ctx, goPath, "build", "-trimpath", "-buildvcs=false", "-ldflags", flags, "-o", filepath.Join(stage, names[component]), "./cmd/"+component)
		c.Dir = source
		c.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOAMD64=v1", "GOFLAGS=", "GOEXPERIMENT=", "GOENV=off", "GOTOOLCHAIN=local", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "SOURCE_DATE_EPOCH="+strconv.FormatInt(at.Unix(), 10))
		if b, e := c.CombinedOutput(); e != nil {
			return zero, fmt.Errorf("build %s: %w: %s", component, e, b)
		}
	}
	digests := map[string]releasecontract.FileDigest{}
	for key, name := range names {
		d, e := Digest(filepath.Join(stage, name))
		if e != nil {
			return zero, e
		}
		digests[key] = d
	}
	m, e := releasecontract.New(strings.TrimPrefix(o.Tag, "v"), revision, digests)
	if e != nil {
		return zero, e
	}
	b, e := json.MarshalIndent(m, "", "  ")
	if e != nil {
		return zero, e
	}
	b = append(b, '\n')
	if _, e = releasecontract.Decode(b); e != nil {
		return zero, e
	}
	if e = os.WriteFile(filepath.Join(stage, releasecontract.ManifestName), b, 0644); e != nil {
		return zero, e
	}
	all := []string{releasecontract.ManifestName}
	for _, n := range names {
		all = append(all, n)
	}
	sort.Strings(all)
	var sums strings.Builder
	for _, name := range all {
		d, e := Digest(filepath.Join(stage, name))
		if e != nil {
			return zero, e
		}
		fmt.Fprintf(&sums, "%s  %s\n", d.SHA256, name)
	}
	if e = os.WriteFile(filepath.Join(stage, "SHA256SUMS"), []byte(sums.String()), 0644); e != nil {
		return zero, e
	}
	// Check again to reject changes while packaging; output has never been in checkout.
	if rev, _, e := Identity(ctx, root, o.Tag); e != nil || rev != revision {
		return zero, errors.New("source changed during release build")
	}
	if e = os.Rename(stage, output); e != nil {
		return zero, e
	}
	return m, nil
}
