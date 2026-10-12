// SPDX-License-Identifier: GPL-3.0-or-later
package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/releasecontract"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const ActivationExit = 75
const FailureExit = 78

// Manager uses an operator-provisioned private directory. Stage never selects an
// executable. Activate only writes the durable request; the launcher tries it.
type Manager struct {
	Root      string
	Client    Client
	Required  []string
	Preflight func(context.Context, releasecontract.Manifest) error
}
type Journal struct {
	SchemaVersion   int       `json:"schema_version"`
	Transaction     string    `json:"transaction"`
	Phase           string    `json:"phase"`
	Current         string    `json:"current"`
	CurrentSHA256   string    `json:"current_sha256"`
	Previous        string    `json:"previous"`
	Candidate       string    `json:"candidate"`
	CandidateSHA256 string    `json:"candidate_sha256"`
	FailedVersion   string    `json:"failed_version"`
	Outcome         string    `json:"outcome"`
	At              time.Time `json:"at"`
}

func (m *Manager) root() error {
	if !filepath.IsAbs(m.Root) || filepath.Clean(m.Root) != m.Root || filepath.Base(m.Root) != "node-releases" {
		return errors.New("operator-provisioned node-releases directory required")
	}
	d, e := artifact.OpenDirectory(m.Root)
	if e != nil {
		return e
	}
	defer d.Close()
	i, e := d.Stat()
	if e != nil {
		return e
	}
	if i.Mode().Perm() != 0700 || i.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return errors.New("node releases must be service-owned mode 0700")
	}
	return nil
}
func (m *Manager) binary(v string) string { return filepath.Join(m.Root, "versions", "v"+v, "s2node") }
func (m *Manager) release(v string) (Release, error) {
	var r Release
	if _, e := releasecontract.ParseVersion(v); e != nil {
		return r, e
	}
	base := filepath.Join(m.Root, "versions", "v"+v)
	b, e := storeio.Read(base, "release.json", releasecontract.MaxManifest+4096)
	if e != nil {
		return r, e
	}
	if e = json.Unmarshal(b, &r); e != nil {
		return r, e
	}
	if r.Version != v || r.Manifest.Version != v || r.Manifest.Validate() != nil || !storeio.Digest(r.ManifestSHA256) || r.SHA256 != r.Manifest.Node().SHA256 || r.AssetURL != r.Manifest.Node().URL || r.SourceRevision != r.Manifest.Source.Revision {
		return r, errors.New("invalid staged release identity")
	}
	hash, size, e := storeio.FileHash(base, "s2node", releasecontract.MaxBinary)
	if e != nil || hash != r.SHA256 || size != r.Manifest.Node().Size {
		return r, errors.New("staged executable checksum/length mismatch")
	}
	d, e := artifact.OpenDirectory(base)
	if e != nil {
		return r, e
	}
	f, e := artifact.OpenRegular(d, "s2node", syscall.O_RDONLY, 0)
	d.Close()
	if e != nil {
		return r, e
	}
	i, e := f.Stat()
	f.Close()
	if e != nil || i.Mode().Perm() != 0500 || i.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return r, errors.New("staged executable owner/mode mismatch")
	}
	return r, nil
}
func Apply(ctx context.Context, r Release, executable string) error {
	root := filepath.Dir(filepath.Dir(filepath.Dir(executable)))
	m := &Manager{Root: root, Client: Client{Enabled: true}}
	if executable != m.binary(r.Version) {
		return errors.New("managed versioned executable path required")
	}
	return m.Stage(ctx, r)
}
func (m *Manager) Stage(ctx context.Context, r Release) error {
	if !m.Client.Enabled {
		return ErrDisabled
	}
	if e := m.root(); e != nil {
		return e
	}
	if !r.verified || r.Manifest.Validate() != nil || r.Version != r.Manifest.Version || r.AssetURL != r.Manifest.Node().URL || r.SHA256 != r.Manifest.Node().SHA256 || !storeio.Digest(r.ManifestSHA256) {
		return errors.New("publisher-verified release required")
	}
	unlock, e := storeio.Lock(ctx, m.Root)
	if e != nil {
		return e
	}
	defer unlock()
	base := filepath.Join(m.Root, "versions")
	if e = os.Mkdir(base, 0700); e != nil && !errors.Is(e, os.ErrExist) {
		return e
	}
	d, e := artifact.OpenDirectory(base)
	if e != nil {
		return e
	}
	defer d.Close()
	entries, e := os.ReadDir(base)
	if e != nil {
		return e
	}
	for _, v := range entries {
		if v.Name() == "v"+r.Version {
			old, e := m.release(r.Version)
			if e != nil {
				return e
			}
			if old.SHA256 != r.SHA256 || old.ManifestSHA256 != r.ManifestSHA256 {
				return errors.New("same-version integrity conflict")
			}
			return nil
		}
	}
	if len(entries) >= 32 {
		return errors.New("release retention quota; operator must prune unreferenced versions offline")
	}
	data, e := m.Client.download(ctx, r.Manifest.Node().Descriptor, releasecontract.MaxBinary)
	if e != nil {
		return e
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	temp := ".stage-" + storeio.ID()
	if e = syscall.Mkdirat(int(d.Fd()), temp, 0700); e != nil {
		return e
	}
	stage := filepath.Join(base, temp)
	defer os.RemoveAll(stage)
	if e = storeio.Write(stage, "s2node", data, 0500); e != nil {
		return e
	}
	if e = storeio.Save(stage, "release.json", r); e != nil {
		return e
	}
	if e = artifact.Rename(d, temp, "v"+r.Version); e != nil {
		return e
	}
	return d.Sync()
}
func (m *Manager) Status() (Journal, error) {
	var j Journal
	if e := m.root(); e != nil {
		return j, e
	}
	b, e := storeio.Read(m.Root, "activation.json", 16<<10)
	if e != nil {
		return j, e
	}
	if e = json.Unmarshal(b, &j); e != nil {
		return j, e
	}
	if j.SchemaVersion != 1 || !storeio.Digest(j.Transaction) || !storeio.Digest(j.CurrentSHA256) {
		return j, errors.New("invalid activation journal")
	}
	if _, e = releasecontract.ParseVersion(j.Current); e != nil {
		return j, e
	}
	if j.Candidate != "" {
		if _, e = releasecontract.ParseVersion(j.Candidate); e != nil {
			return j, e
		}
		if !storeio.Digest(j.CandidateSHA256) {
			return j, errors.New("missing candidate digest")
		}
	}
	switch j.Phase {
	case "good", "pending", "trial-started", "rollback-started", "failed":
	default:
		return j, errors.New("unknown activation phase")
	}
	if (j.Phase == "pending" || j.Phase == "trial-started") && j.Candidate == "" {
		return j, errors.New("missing candidate")
	}
	return j, nil
}
func (m *Manager) save(j Journal) error { return storeio.Save(m.Root, "activation.json", j) }
func (m *Manager) Initialize(ctx context.Context, version string) error {
	if e := m.root(); e != nil {
		return e
	}
	unlock, e := storeio.Lock(ctx, m.Root)
	if e != nil {
		return e
	}
	defer unlock()
	if _, e = m.Status(); !errors.Is(e, os.ErrNotExist) {
		if e == nil {
			return errors.New("managed installation already initialized")
		}
		return e
	}
	r, e := m.release(version)
	if e != nil {
		return e
	}
	return m.save(Journal{SchemaVersion: 1, Transaction: storeio.Hash([]byte(storeio.ID())), Phase: "good", Current: version, CurrentSHA256: r.SHA256, Outcome: "initialized", At: time.Now().UTC()})
}
func (m *Manager) Activate(ctx context.Context, version string) (Journal, error) {
	var j Journal
	if !m.Client.Enabled {
		return j, ErrDisabled
	}
	if e := m.root(); e != nil {
		return j, e
	}
	unlock, e := storeio.Lock(ctx, m.Root)
	if e != nil {
		return j, e
	}
	defer unlock()
	j, e = m.Status()
	if e != nil {
		return j, e
	}
	if j.Phase != "good" {
		return j, errors.New("activation already pending or recovery required")
	}
	old, e := m.release(j.Current)
	if e != nil || old.SHA256 != j.CurrentSHA256 {
		return j, errors.New("current verified release unavailable")
	}
	r, e := m.release(version)
	if e != nil {
		return j, e
	}
	if version == j.Current {
		return j, errors.New("release already current")
	}
	if version == j.FailedVersion {
		return j, errors.New("failed candidate requires operator review before another attempt")
	}
	if !m.Client.AllowDowngrade && releasecontract.Compare(version, j.Current) < 0 {
		return j, errors.New("downgrade not permitted")
	}
	if e = releasecontract.Compatible(old.Manifest, r.Manifest, m.Required); e != nil {
		return j, e
	}
	if m.Preflight != nil {
		if e = m.Preflight(ctx, r.Manifest); e != nil {
			return j, e
		}
	}
	if ctx.Err() != nil {
		return j, ctx.Err()
	}
	j.Transaction = storeio.Hash([]byte(storeio.ID()))
	j.Phase = "pending"
	j.Candidate = version
	j.CandidateSHA256 = r.SHA256
	j.Outcome = "activation_requested"
	j.At = time.Now().UTC()
	return j, m.save(j)
}
