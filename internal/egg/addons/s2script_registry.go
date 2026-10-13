// SPDX-License-Identifier: GPL-3.0-or-later
package addons

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// S2ScriptRegistryURL may be replaced by an embedding application for a trusted
// mirror. Requests and redirects must remain on this HTTPS origin.
var S2ScriptRegistryURL = "https://www.s2script.com"

type s2PolicyEntry struct {
	Source  string   `json:"source"`
	Version string   `json:"version"`
	Enabled *bool    `json:"enabled,omitempty"`
	With    []string `json:"with,omitempty"`
}
type s2Policy struct {
	SchemaVersion int                      `json:"schema_version"`
	Plugins       map[string]s2PolicyEntry `json:"plugins"`
}
type s2RootMember struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}
type s2RegistryRoot struct {
	Name     string         `json:"name"`
	Version  string         `json:"version"`
	Range    string         `json:"range"`
	With     []string       `json:"with"`
	Registry string         `json:"registry"`
	Members  []s2RootMember `json:"members"`
}
type s2Host struct {
	Version        string            `json:"version"`
	SourceRevision string            `json:"source_revision"`
	Files          map[string]string `json:"files"`
}
type s2Lock struct {
	SchemaVersion int              `json:"schema_version"`
	Plugins       []s2Installed    `json:"plugins"`
	Roots         []s2RegistryRoot `json:"registry_roots,omitempty"`
	Host          *s2Host          `json:"host,omitempty"`
}
type s2PlanEntry struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	Filename    string `json:"filename"`
	ReviewState string `json:"reviewState"`
}
type s2Plan struct {
	Root *struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"root"`
	Install []s2PlanEntry `json:"install"`
	Errors  []string      `json:"errors"`
}
type s2Check struct {
	Name      string `json:"name"`
	Installed string `json:"installed"`
	Available string `json:"available,omitempty"`
	Enabled   bool   `json:"enabled"`
	Status    string `json:"status"`
}
type s2HTTPError int

func (e s2HTTPError) Error() string { return fmt.Sprintf("registry HTTP %d", int(e)) }
func s2Unavailable(err error) bool {
	message := strings.ToLower(err.Error())
	if errors.Is(err, context.Canceled) || strings.Contains(message, "tls") || strings.Contains(message, "x509") || strings.Contains(message, "certificate") {
		return false
	}
	var cert *tls.CertificateVerificationError
	var authority x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	if errors.As(err, &cert) || errors.As(err, &authority) || errors.As(err, &invalid) || errors.As(err, &hostname) {
		return false
	}
	var code s2HTTPError
	var network net.Error
	var op *net.OpError
	var dns *net.DNSError
	var address net.InvalidAddrError
	if errors.As(err, &address) {
		return false
	}
	return errors.As(err, &code) && (code == 429 || code >= 500 && code <= 599) || errors.As(err, &op) && (op.Op == "dial" || op.Op == "read" || op.Op == "write") || errors.As(err, &dns) || errors.As(err, &network) && network.Timeout()
}

// AcquireS2ScriptRuntime excludes offline plugin writers for the managed game
// lifetime. Startup checks assume this lease is already held by the entrypoint.
func AcquireS2ScriptRuntime(root string) (func(), error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	base, err := os.Open("/")
	if err != nil {
		return nil, err
	}
	defer base.Close()
	dir, err := s2OpenDir(base, strings.TrimPrefix(abs, "/"), false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	fd, err := unix.Openat(int(dir.Fd()), ".s2script-runtime", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), ".s2script-runtime")
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("unsafe S2Script runtime lock")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("S2Script runtime is busy; stop the server before changing plugins")
	}
	return func() { unix.Flock(fd, unix.LOCK_UN); f.Close() }, nil
}

func s2PolicyPath(getenv func(string) string) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	name := "s2script-plugins.json"
	if getenv != nil && getenv("S2SCRIPT_PLUGIN_FILE") != "" {
		name = getenv("S2SCRIPT_PLUGIN_FILE")
	}
	if !filepath.IsLocal(name) || filepath.Clean(name) != name || strings.ContainsAny(name, "\\\x00:") {
		return "", errors.New("S2SCRIPT_PLUGIN_FILE must be a safe server-root-relative path")
	}
	return name, nil
}
func (s *s2PluginStore) readJSON(path string, v any) error {
	dir, err := s2OpenDir(s.root, filepath.Dir(path), false)
	if err != nil {
		return err
	}
	defer dir.Close()
	b, err := s2ReadAt(dir, filepath.Base(path), 4<<20)
	if err != nil {
		return err
	}
	return s2JSON(b, v)
}
func (s *s2PluginStore) writeJSON(path string, v any) error {
	dir, err := s2OpenDir(s.root, filepath.Dir(path), true)
	if err != nil {
		return err
	}
	defer dir.Close()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > 4<<20 {
		return errors.New("plugin metadata exceeds limit")
	}
	return s2WriteAt(dir, filepath.Base(path), append(b, '\n'))
}
func (s *s2PluginStore) policy(path string) (s2Policy, error) {
	p := s2Policy{1, map[string]s2PolicyEntry{}}
	if err := s.readJSON(path, &p); err != nil && !errors.Is(err, unix.ENOENT) {
		return p, err
	}
	var fields map[string]json.RawMessage
	if err := s.readJSON(path, &fields); err == nil {
		for key := range fields {
			if key != "schema_version" && key != "plugins" {
				return p, fmt.Errorf("unknown plugin policy field %q", key)
			}
		}
		var entries map[string]map[string]json.RawMessage
		if err = json.Unmarshal(fields["plugins"], &entries); err != nil {
			return p, err
		}
		for name, entry := range entries {
			if entry == nil {
				return p, errors.New("null plugin policy entry")
			}
			for key, raw := range entry {
				if key != "source" && key != "version" && key != "enabled" && key != "with" {
					return p, fmt.Errorf("unknown plugin policy field %q for %s", key, name)
				}
				if string(raw) == "null" {
					return p, errors.New("null plugin policy fields are invalid")
				}
			}
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return p, err
	}
	if p.SchemaVersion != 1 || p.Plugins == nil {
		return p, errors.New("invalid plugin policy schema")
	}
	for name, e := range p.Plugins {
		if !s2PluginName.MatchString(name) || (e.Source != "registry" && e.Source != "release" && e.Source != "local") {
			return p, errors.New("invalid plugin policy identity/source")
		}
		v, err := s2NormalizeVersion(e.Version)
		if err != nil {
			return p, err
		}
		e.Version = v
		seen := map[string]bool{}
		for _, optional := range e.With {
			if !s2PluginName.MatchString(optional) || seen[optional] {
				return p, errors.New("invalid optional plugin selection")
			}
			seen[optional] = true
		}
		p.Plugins[name] = e
	}
	return p, nil
}
func (s *s2PluginStore) retained(actual []s2Installed) ([]s2Installed, error) {
	l := s2Lock{SchemaVersion: 1, Plugins: []s2Installed{}}
	if err := s.readJSON("s2script-plugins.lock.json", &l); err != nil && !errors.Is(err, unix.ENOENT) {
		return nil, err
	}
	if l.SchemaVersion != 1 || l.Plugins == nil {
		return nil, errors.New("invalid plugin lock schema")
	}
	s.roots = l.Roots
	s.host = l.Host
	old := map[string]s2Installed{}
	for _, p := range l.Plugins {
		if !s2PluginName.MatchString(p.Name) || !s2PluginFilename.MatchString(p.Filename) || !s2Hash(p.SHA256) || old[p.Name].Name != "" {
			return nil, errors.New("invalid or duplicate plugin lock facts")
		}
		var m s2Manifest
		if err := s2JSON(p.Manifest, &m); err != nil || m.ID != p.Name || m.Version != p.Version {
			return nil, errors.New("plugin lock manifest does not match identity/version")
		}
		if p.Source != "registry" && p.Source != "release" && p.Source != "local" {
			return nil, errors.New("invalid plugin lock source")
		}
		if p.Managed && (p.Verification != "verified" || p.Source == "local") {
			return nil, errors.New("plugin ownership lacks verified source")
		}
		p.Path = s2Target(p.Filename, p.Enabled)
		p.APIVersion = m.APIVersion
		old[p.Name] = p
	}
	for i, p := range actual {
		if o, ok := old[p.Name]; ok {
			delete(old, p.Name)
			if p.Version == o.Version && p.SHA256 == o.SHA256 && p.Filename == o.Filename {
				o.Path = p.Path
				o.Enabled = p.Enabled
				o.Manifest = p.Manifest
				if o.LastCheckStatus == "missing" {
					o.LastCheckStatus = "rediscovered"
				}
				actual[i] = o
			} else {
				actual[i].LastCheckStatus = "modified"
			}
		}
	}
	for _, p := range old {
		p.LastCheckStatus = "missing"
		actual = append(actual, p)
	}
	sort.Slice(actual, func(i, j int) bool { return actual[i].Name < actual[j].Name })
	return actual, nil
}
func (s *s2PluginStore) save(all []s2Installed) error {
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	lock, err := s2ReadAt(s.root, "s2script-plugins.lock.json", 4<<20)
	lockMissing := errors.Is(err, unix.ENOENT)
	if err != nil && !lockMissing {
		return err
	}
	inventory, err := s2ReadAt(s.egg, "installed-list.json", 4<<20)
	inventoryMissing := errors.Is(err, unix.ENOENT)
	if err != nil && !inventoryMissing {
		return err
	}
	if err = s.writeJSON("s2script-plugins.lock.json", s2Lock{SchemaVersion: 1, Plugins: all, Roots: s.roots, Host: s.host}); err != nil {
		return err
	}
	if err = s.inventory(all); err != nil {
		if lockMissing {
			unix.Unlinkat(int(s.root.Fd()), "s2script-plugins.lock.json", 0)
		} else {
			s2WriteAt(s.root, "s2script-plugins.lock.json", lock)
		}
		if inventoryMissing {
			unix.Unlinkat(int(s.egg.Fd()), "installed-list.json", 0)
		} else {
			s2WriteAt(s.egg, "installed-list.json", inventory)
		}
		return err
	}
	return nil
}

// S2ScriptReleasePins lets startup constrain the native release before any
// framework download or writes. The updater validates one common exact pin.
func S2ScriptReleasePins(root string, getenv func(string) string) (map[string]string, error) {
	path, err := s2PolicyPath(getenv)
	if err != nil {
		return nil, err
	}
	s, err := s2OpenStore(root)
	if err != nil {
		return nil, err
	}
	defer s.close()
	p, err := s.policy(path)
	if err != nil {
		return nil, err
	}
	pins := map[string]string{}
	for name, entry := range p.Plugins {
		if entry.Source == "release" && entry.Version != "" && entry.Version != "*" {
			pins[name] = entry.Version
		}
	}
	return pins, nil
}

// RegisterS2ScriptReleasePlugins records only packages copied from a verified
// release by the native updater. Other files retain their ownership and bytes.
func RegisterS2ScriptReleasePlugins(root string, files map[string]string) error {
	s, err := s2OpenStore(root)
	if err != nil {
		return err
	}
	defer s.close()
	all, err := s.discover()
	if err != nil {
		return err
	}
	all, err = s.retained(all)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, p := range S2ScriptPlugins() {
		known[p.Name] = true
	}
	left := len(files)
	for i, p := range all {
		if hash, ok := files[p.Path]; ok {
			if !known[p.Name] || p.LastCheckStatus == "missing" || hash != p.SHA256 {
				return errors.New("release plugin registration does not match actual artifact")
			}
			all[i].Source = "release"
			all[i].Managed = true
			all[i].Verification = "verified"
			left--
		}
	}
	if left != 0 {
		return errors.New("release plugin registration references missing packages")
	}
	return s.save(all)
}

var s2NativePaths = []string{"game/csgo/addons/metamod/s2script.vdf", "game/csgo/addons/s2script/bin/linuxsteamrt64/s2script.so", "game/csgo/addons/s2script/bin/linuxsteamrt64/libs2script_core.so"}

func (s *s2PluginStore) hostVerified() bool {
	if s.host == nil || !s2ExactVersion(s.host.Version) || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(s.host.SourceRevision) || len(s.host.Files) != len(s2NativePaths) {
		return false
	}
	for _, path := range s2NativePaths {
		hash := s.host.Files[path]
		if !s2Hash(hash) {
			return false
		}
		dir, err := s2OpenDir(s.root, filepath.Dir(path), false)
		if err != nil {
			return false
		}
		data, err := s2ReadAt(dir, filepath.Base(path), 256<<20)
		dir.Close()
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != hash {
			return false
		}
	}
	return true
}

// RegisterS2ScriptHost is called only after the updater verifies the immutable
// release's source declares the supported host3/API2 compatibility family.
func RegisterS2ScriptHost(root, version, sourceRevision string, native map[string]string) error {
	s, err := s2OpenStore(root)
	if err != nil {
		return err
	}
	defer s.close()
	all, err := s.discover()
	if err != nil {
		return err
	}
	all, err = s.retained(all)
	if err != nil {
		return err
	}
	s.host = &s2Host{Version: strings.TrimPrefix(version, "v"), SourceRevision: sourceRevision, Files: native}
	if !s.hostVerified() {
		return errors.New("verified host source/native evidence is incomplete or changed")
	}
	return s.save(all)
}
func S2ScriptHostVerified(root, version string) bool {
	s, err := s2OpenStore(root)
	if err != nil {
		return false
	}
	defer s.close()
	if _, err = s.retained(nil); err != nil {
		return false
	}
	return s.host != nil && s.host.Version == strings.TrimPrefix(version, "v") && s.hostVerified()
}

type s2Registry struct {
	client   *http.Client
	base     *url.URL
	reviewed bool
}

func (e *Env) s2Registry(getenv func(string) string) (*s2Registry, error) {
	base, err := url.Parse(S2ScriptRegistryURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("registry needs a trusted HTTPS origin")
	}
	r := &s2Registry{base: base}
	if getenv != nil {
		switch getenv("S2SCRIPT_REVIEWED_ONLY") {
		case "", "0":
		case "1":
			r.reviewed = true
		default:
			return nil, errors.New("S2SCRIPT_REVIEWED_ONLY must be 0 or 1")
		}
	}
	client := e.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	clone := *client
	clone.Jar = nil
	if clone.Timeout <= 0 {
		clone.Timeout = 30 * time.Second
	}
	callback := clone.CheckRedirect
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many registry redirects")
		}
		if err := r.trust(req.URL.String()); err != nil {
			return err
		}
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
		if callback != nil {
			return callback(req, via)
		}
		return nil
	}
	r.client = &clone
	return r, nil
}
func (r *s2Registry) trust(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, r.base.Host) || u.User != nil || u.Fragment != "" {
		return errors.New("registry artifact/redirect leaves trusted HTTPS origin")
	}
	return nil
}
func (r *s2Registry) get(ctx context.Context, raw string, limit int64) ([]byte, error) {
	if err := r.trust(raw); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, s2HTTPError(resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("registry response exceeds limit")
	}
	return b, nil
}
func (r *s2Registry) endpoint(path string, q url.Values) string {
	u := *r.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = q.Encode()
	return u.String()
}
func (r *s2Registry) plan(ctx context.Context, name, selector string, with []string) (s2Plan, error) {
	var p s2Plan
	q := url.Values{"name": {name}, "range": {selector}}
	if len(with) > 0 {
		q.Set("with", strings.Join(with, ","))
	}
	b, err := r.get(ctx, r.endpoint("/api/v1/plan", q), 4<<20)
	if err != nil {
		return p, err
	}
	if err = s2JSON(b, &p); err != nil {
		return p, err
	}
	if p.Root == nil || p.Root.Name != name || !s2Satisfies(p.Root.Version, selector) || p.Install == nil || len(p.Errors) > 0 {
		return p, errors.New("registry plan does not satisfy requested root")
	}
	seen := map[string]bool{}
	files := map[string]bool{}
	for _, a := range p.Install {
		if !s2PluginName.MatchString(a.Name) || !s2ExactVersion(a.Version) || !s2Hash(a.SHA256) || !s2PluginFilename.MatchString(a.Filename) || len(a.Filename) > 240 || seen[a.Name] || files[a.Filename] || a.ReviewState == "" {
			return p, errors.New("invalid registry plan artifact")
		}
		if a.Name == name && a.Version != p.Root.Version {
			return p, errors.New("registry root artifact version conflict")
		}
		if r.reviewed && a.ReviewState != "reviewed" {
			return p, errors.New("registry artifact is not reviewed")
		}
		if err = r.trust(a.URL); err != nil {
			return p, err
		}
		seen[a.Name] = true
		files[a.Filename] = true
	}
	return p, nil
}
func s2Hash(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && s == strings.ToLower(s)
}

func s2Effective(name string, current bool, policy s2Policy, panel map[string]bool) bool {
	if e, ok := policy.Plugins[name]; ok && e.Enabled != nil {
		current = *e.Enabled
	}
	if enabled, ok := panel[name]; ok {
		current = enabled
	}
	return current
}

func s2ValidatePolicyFacts(policy s2Policy, all []s2Installed, registry bool) error {
	facts := map[string]s2Installed{}
	for _, p := range all {
		facts[p.Name] = p
	}
	for name, entry := range policy.Plugins {
		if !registry && entry.Source == "registry" {
			continue
		}
		p, ok := facts[name]
		if !ok || p.LastCheckStatus == "missing" {
			if entry.Source == "registry" {
				continue
			}
			return fmt.Errorf("desired %s plugin %s is missing", entry.Source, name)
		}
		if p.Source != entry.Source {
			return fmt.Errorf("desired source %s conflicts with installed %s for %s", entry.Source, p.Source, name)
		}
		if !s2Satisfies(p.Version, entry.Version) {
			return fmt.Errorf("installed %s version does not satisfy policy %s", name, entry.Version)
		}
	}
	return nil
}

// The currently supported S2Script host family exposes API 3 and its API 2
// compatibility loader. Interface protocol 2 requires the current API.
func s2Compatible(m s2Manifest, raw []byte) error {
	if !regexp.MustCompile(`^[23]\.(?:[xX*]|[0-9]+\.[xX*])$`).MatchString(m.APIVersion) {
		if _, err := s2NormalizeVersion(m.APIVersion); err != nil {
			return errors.New("invalid S2Script plugin API version")
		}
	}
	api := strings.TrimLeft(m.APIVersion, "<>=^~ ")
	major := ""
	if strings.HasPrefix(api, "2.") {
		major = "2"
	}
	if strings.HasPrefix(api, "3.") {
		major = "3"
	}
	if major == "" {
		return fmt.Errorf("unsupported S2Script plugin API %q", m.APIVersion)
	}
	var extra struct {
		InterfaceProtocol int `json:"interfaceProtocol"`
	}
	if err := s2JSON(raw, &extra); err != nil {
		return err
	}
	if extra.InterfaceProtocol < 0 || extra.InterfaceProtocol > 2 || extra.InterfaceProtocol == 2 && major != "3" {
		return errors.New("unsupported S2Script interface protocol/API combination")
	}
	return nil
}

// s2Dependencies resolves manifest interfaces using their published versions,
// independently of package versions, and checks compilation type hashes.
func s2Dependencies(all []s2Installed, legacy bool) (map[string][]string, error) {
	type provider struct{ name, version, hash string }
	providers := map[string]provider{}
	manifests := map[string]s2Manifest{}
	graph := map[string][]string{}
	for _, p := range all {
		if p.LastCheckStatus == "missing" {
			continue
		}
		var m s2Manifest
		if err := s2JSON(p.Manifest, &m); err != nil {
			return nil, err
		}
		if err := s2Compatible(m, p.Manifest); err != nil {
			return nil, err
		}
		manifests[p.Name] = m
		for name, published := range m.Publishes {
			if _, ok := providers[name]; ok {
				return nil, fmt.Errorf("multiple plugin providers publish %s", name)
			}
			if !s2PluginName.MatchString(name) || !s2ExactVersion(published.Version) || published.TypesSHA256 != "" && !s2Hash(published.TypesSHA256) {
				return nil, errors.New("invalid published interface version")
			}
			providers[name] = provider{p.Name, published.Version, published.TypesSHA256}
		}
	}
	for name, m := range manifests {
		for dep, selector := range m.Dependencies {
			if legacy && strings.HasPrefix(strings.TrimLeft(m.APIVersion, "<>=^~ "), "2.") && (dep == "@s2script/entity" || dep == "@s2script/math") {
				continue
			}
			p, ok := providers[dep]
			if !ok {
				return nil, fmt.Errorf("%s requires missing published interface %s", name, dep)
			}
			if !s2Satisfies(p.version, selector) {
				return nil, fmt.Errorf("%s requires incompatible interface %s", name, dep)
			}
			graph[name] = append(graph[name], p.name)
		}
		for dep, hash := range m.CompiledAgainst {
			if p, ok := providers[dep]; ok && hash != "" && hash != p.hash {
				return nil, fmt.Errorf("plugin compiledAgainst hash mismatch: %s", dep)
			}
		}
		for dep, selector := range m.OptionalDependencies {
			if p, ok := providers[dep]; ok && !s2Satisfies(p.version, selector) {
				return nil, fmt.Errorf("incompatible optional interface %s", dep)
			}
		}
	}
	state := map[string]int{}
	var visit func(string) error
	visit = func(name string) error {
		if state[name] == 1 {
			return errors.New("plugin dependency cycle")
		}
		if state[name] == 2 {
			return nil
		}
		state[name] = 1
		for _, dep := range graph[name] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[name] = 2
		return nil
	}
	for name := range manifests {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return graph, nil
}
func s2ValidateEnabled(all []s2Installed, graph map[string][]string) error {
	enabled := map[string]bool{}
	for _, p := range all {
		enabled[p.Name] = p.Enabled && p.LastCheckStatus != "missing"
	}
	for name, deps := range graph {
		if enabled[name] {
			for _, dep := range deps {
				if !enabled[dep] {
					return fmt.Errorf("enabled %s requires disabled %s", name, dep)
				}
			}
		}
	}
	return nil
}

func s2EnableProviders(all []s2Installed, graph map[string][]string, policy s2Policy, panel map[string]bool) error {
	for pass := 0; pass < len(all); pass++ {
		changed := false
		for _, p := range all {
			if !p.Enabled {
				continue
			}
			for _, dep := range graph[p.Name] {
				for i, provider := range all {
					if provider.Name != dep || provider.Enabled {
						continue
					}
					if !s2Effective(dep, true, policy, panel) {
						return fmt.Errorf("explicitly disabled dependency %s required by %s", dep, p.Name)
					}
					all[i].Enabled = true
					all[i].Path = s2Target(provider.Filename, true)
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}
	return s2ValidateEnabled(all, graph)
}

func (s *s2PluginStore) replay(r *s2Registry, policy s2Policy, all []s2Installed, panel map[string]bool, roots map[string]bool, checks []s2Check, apply bool) ([]s2Installed, []s2Check, error) {
	facts := map[string]s2Installed{}
	for _, p := range all {
		facts[p.Name] = p
	}
	evidence := map[string]s2RegistryRoot{}
	for _, root := range s.roots {
		if evidence[root.Name].Name != "" {
			return nil, nil, errors.New("duplicate retained registry root")
		}
		evidence[root.Name] = root
	}
	for name := range roots {
		entry := policy.Plugins[name]
		selector := entry.Version
		if selector == "" {
			selector = "*"
		}
		root, ok := evidence[name]
		if !ok || root.Range != selector || !s2Satisfies(root.Version, selector) || strings.TrimRight(root.Registry, "/") != strings.TrimRight(r.base.String(), "/") || len(root.Members) == 0 {
			return nil, nil, fmt.Errorf("no matching verified retained registry closure for %s", name)
		}
		a, b := append([]string(nil), root.With...), append([]string(nil), entry.With...)
		sort.Strings(a)
		sort.Strings(b)
		if strings.Join(a, "\x00") != strings.Join(b, "\x00") {
			return nil, nil, errors.New("retained optional plugin selection changed")
		}
		seen := map[string]bool{}
		for _, member := range root.Members {
			p, ok := facts[member.Name]
			if seen[member.Name] || !ok || p.LastCheckStatus == "missing" || !p.Managed || p.Verification != "verified" || p.Source != "registry" || p.Version != member.Version || p.SHA256 != member.SHA256 || r.trust(p.SourceURL) != nil || r.reviewed && p.ReviewState != "reviewed" {
				return nil, nil, errors.New("retained registry closure is incomplete, modified, or unverified")
			}
			seen[member.Name] = true
		}
	}
	result := append([]s2Installed(nil), all...)
	for i, p := range result {
		if p.Source == "registry" && (!p.Managed || p.Verification != "verified" || p.LastCheckStatus == "missing" || r.trust(p.SourceURL) != nil || r.reviewed && p.ReviewState != "reviewed") {
			return nil, nil, errors.New("retained installed registry artifact is unavailable or unverified")
		}
		result[i].Enabled = s2Effective(p.Name, p.Enabled, policy, panel)
		result[i].Path = s2Target(p.Filename, result[i].Enabled)
		if p.Source == "registry" {
			result[i].LastCheckStatus = "retained-unavailable"
			result[i].AvailableVersion = ""
			result[i].LastCheckTime = time.Now().UTC().Format(time.RFC3339)
		}
	}
	if err := s2ValidatePolicyFacts(policy, result, true); err != nil {
		return nil, nil, err
	}
	graph, err := s2Dependencies(result, s.hostVerified())
	if err != nil {
		return nil, nil, err
	}
	if err = s2EnableProviders(result, graph, policy, panel); err != nil {
		return nil, nil, err
	}
	for i := range checks {
		checks[i].Status = "retained-unavailable"
		checks[i].Available = ""
	}
	if apply {
		if err = s.s2Apply(all, result, nil); err != nil {
			return nil, nil, err
		}
	}
	return result, checks, nil
}

func (e *Env) s2Reconcile(ctx context.Context, s *s2PluginStore, policy s2Policy, all []s2Installed, panel map[string]bool, getenv func(string) string, apply bool) ([]s2Installed, []s2Check, error) {
	r, err := e.s2Registry(getenv)
	if err != nil {
		return nil, nil, err
	}
	checks := []s2Check{}
	plans := map[string]s2Plan{}
	roots := map[string]bool{}
	outage := false
	for _, p := range all {
		if p.Managed && p.Source == "registry" && len(p.Parents) == 0 {
			if _, desired := policy.Plugins[p.Name]; !desired {
				roots[p.Name] = true
			}
		}
	}
	for name, entry := range policy.Plugins {
		if entry.Source == "registry" {
			roots[name] = true
		}
	}
	// Check every observed/retained identity, active and disabled. Discovery never
	// adopts manual packages, even when their IDs exist in the public registry.
	for _, p := range all {
		check := s2Check{Name: p.Name, Installed: p.Version, Enabled: p.Enabled, Status: "current"}
		b, err := r.get(ctx, r.endpoint("/api/v1/meta", url.Values{"name": {p.Name}}), 4<<20)
		if err == nil {
			var meta any
			err = s2JSON(b, &meta)
		}
		var status s2HTTPError
		if err != nil {
			if s2Unavailable(err) {
				outage = true
				check.Status = "unavailable"
				checks = append(checks, check)
				continue
			}
			if p.Source != "registry" && errors.As(err, &status) && status == 404 {
				check.Status = "not-found"
				checks = append(checks, check)
				continue
			}
			return nil, nil, fmt.Errorf("check %s: %w", p.Name, err)
		}
		plan, err := r.plan(ctx, p.Name, "*", nil)
		if err != nil {
			if s2Unavailable(err) {
				outage = true
				check.Status = "unavailable"
				checks = append(checks, check)
				continue
			}
			if p.Source != "registry" && errors.As(err, &status) && status == 404 {
				check.Status = "not-found"
				checks = append(checks, check)
				continue
			}
			return nil, nil, fmt.Errorf("check %s: %w", p.Name, err)
		}
		check.Available = plan.Root.Version
		if p.Version != check.Available {
			check.Status = "update-available"
		}
		if p.LastCheckStatus == "missing" {
			check.Status = "missing"
		}
		checks = append(checks, check)
	}
	for name := range roots {
		entry, exists := policy.Plugins[name]
		selector := "*"
		if exists {
			if entry.Source != "registry" {
				continue
			}
			selector = entry.Version
			if selector == "" {
				selector = "*"
			}
		}
		plan, err := r.plan(ctx, name, selector, entry.With)
		if err != nil {
			if s2Unavailable(err) {
				outage = true
				continue
			}
			return nil, nil, fmt.Errorf("resolve %s: %w", name, err)
		}
		if len(plan.Install) == 0 {
			return nil, nil, fmt.Errorf("registry root %s has no verified artifact closure", name)
		}
		plans[name] = plan
	}
	if outage {
		return s.replay(r, policy, all, panel, roots, checks, apply)
	}
	if !apply {
		return all, checks, nil
	}
	actual := map[string]s2Installed{}
	for _, p := range all {
		actual[p.Name] = p
	}
	selected := map[string]s2PlanEntry{}
	selectedEnabled := map[string]bool{}
	parents := map[string][]string{}
	for name, plan := range plans {
		current := true
		if p, ok := actual[name]; ok {
			current = p.Enabled
		}
		enabled := s2Effective(name, current, policy, panel)
		for _, a := range plan.Install {
			if previous, ok := selected[a.Name]; ok && previous != a {
				return nil, nil, fmt.Errorf("registry roots disagree about %s", a.Name)
			}
			selected[a.Name] = a
			selectedEnabled[a.Name] = selectedEnabled[a.Name] || enabled
			if a.Name != name {
				parents[a.Name] = append(parents[a.Name], name)
			}
		}
	}
	payloads := map[string][]byte{}
	total := 0
	for name, a := range selected {
		p, exists := actual[name]
		if exists && !p.Managed {
			entry, explicit := policy.Plugins[name]
			if !explicit || entry.Source != "registry" || p.LastCheckStatus == "missing" || p.Source != "local" || p.Version != a.Version || p.SHA256 != a.SHA256 {
				return nil, nil, fmt.Errorf("registry refuses to overwrite unowned local plugin %s", name)
			}
			p.Source = "registry"
			p.Managed = true
			p.Verification = "verified"
		}
		if exists && p.Source != "registry" {
			return nil, nil, fmt.Errorf("registry conflicts with release plugin %s", name)
		}
		if entry, ok := policy.Plugins[name]; ok && entry.Source != "registry" {
			return nil, nil, fmt.Errorf("registry dependency conflicts with %s source", name)
		}
		if entry, ok := policy.Plugins[name]; ok && !s2Satisfies(a.Version, entry.Version) {
			return nil, nil, fmt.Errorf("registry artifact %s violates policy version %s", name, entry.Version)
		}
		enabled := selectedEnabled[name]
		if exists {
			enabled = p.Enabled
		}
		enabled = s2Effective(name, enabled, policy, panel)
		if exists && p.SHA256 == a.SHA256 && p.Version == a.Version && p.LastCheckStatus != "missing" {
			p.Enabled = enabled
			p.Path = s2Target(p.Filename, enabled)
			p.Parents = parents[name]
			p.ReviewState = a.ReviewState
			p.SourceURL = a.URL
			actual[name] = p
			continue
		}
		data, err := r.get(ctx, a.URL, s2MaxPlugin)
		if err != nil {
			if s2Unavailable(err) {
				return s.replay(r, policy, all, panel, roots, checks, apply)
			}
			return nil, nil, err
		}
		total += len(data)
		if total > 256<<20 {
			return nil, nil, errors.New("registry closure exceeds size limit")
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != a.SHA256 {
			return nil, nil, errors.New("registry artifact SHA-256 mismatch")
		}
		m, raw, err := s2Inspect(data)
		if err != nil {
			return nil, nil, err
		}
		if m.ID != a.Name || m.Version != a.Version {
			return nil, nil, errors.New("registry artifact manifest differs from plan")
		}
		payloads[name] = data
		actual[name] = s2Installed{Name: name, Version: a.Version, APIVersion: m.APIVersion, Enabled: enabled, Path: s2Target(a.Filename, enabled), Filename: a.Filename, SHA256: a.SHA256, Source: "registry", Managed: true, Verification: "verified", LastCheckStatus: "verified", Manifest: raw, Parents: parents[name], SourceURL: a.URL, ReviewState: a.ReviewState}
	}
	result := []s2Installed{}
	checkByName := map[string]s2Check{}
	for _, check := range checks {
		checkByName[check.Name] = check
	}
	for _, p := range actual {
		if _, ok := selected[p.Name]; !ok {
			p.Enabled = s2Effective(p.Name, p.Enabled, policy, panel)
			p.Path = s2Target(p.Filename, p.Enabled)
		}
		if check, ok := checkByName[p.Name]; ok {
			p.LastCheckStatus = check.Status
			if _, downloaded := payloads[p.Name]; downloaded {
				p.LastCheckStatus = "verified"
			}
			p.AvailableVersion = check.Available
			p.LastCheckTime = time.Now().UTC().Format(time.RFC3339)
		}
		result = append(result, p)
	}
	if err = s2ValidatePolicyFacts(policy, result, true); err != nil {
		return nil, nil, err
	}
	graph, err := s2Dependencies(result, s.hostVerified())
	if err != nil {
		return nil, nil, err
	}
	if err = s2EnableProviders(result, graph, policy, panel); err != nil {
		return nil, nil, err
	}
	verifiedRoots := []s2RegistryRoot{}
	for name, plan := range plans {
		entry := policy.Plugins[name]
		selector := entry.Version
		if selector == "" {
			selector = "*"
		}
		root := s2RegistryRoot{Name: name, Version: plan.Root.Version, Range: selector, With: append([]string{}, entry.With...), Registry: r.base.String(), Members: []s2RootMember{}}
		for _, a := range plan.Install {
			root.Members = append(root.Members, s2RootMember{a.Name, a.Version, a.SHA256})
		}
		verifiedRoots = append(verifiedRoots, root)
	}
	sort.Slice(verifiedRoots, func(i, j int) bool { return verifiedRoots[i].Name < verifiedRoots[j].Name })
	s.roots = verifiedRoots
	if err = s.s2Apply(all, result, payloads); err != nil {
		return nil, nil, err
	}
	return result, checks, nil
}

// All downloads/manifest checks precede live changes. A failed write rolls back
// previous package writes and moves; metadata is committed only afterwards.
func (s *s2PluginStore) s2Apply(before, after []s2Installed, payloads map[string][]byte) error {
	oldByName := map[string]s2Installed{}
	oldByPath := map[string]s2Installed{}
	destinations := map[string]bool{}
	for _, p := range before {
		oldByName[p.Name] = p
		if p.LastCheckStatus != "missing" {
			data, err := s2ReadAt(s.dir(p.Enabled), p.Filename, s2MaxPlugin)
			if err != nil {
				return err
			}
			if fmt.Sprintf("%x", sha256.Sum256(data)) != p.SHA256 {
				return fmt.Errorf("plugin changed during inspection: %s", p.Name)
			}
			oldByPath[p.Path] = p
		}
	}
	for _, p := range after {
		if p.LastCheckStatus == "missing" {
			continue
		}
		if destinations[p.Path] {
			return errors.New("plugin destination collision")
		}
		destinations[p.Path] = true
		if occupant, ok := oldByPath[p.Path]; ok && occupant.Name != p.Name {
			return errors.New("plugin destination belongs to another package")
		}
		if err := s2CheckFile(s.dir(p.Enabled), p.Filename); err != nil {
			return err
		}
		if _, known := oldByPath[p.Path]; !known {
			var st unix.Stat_t
			err := unix.Fstatat(int(s.dir(p.Enabled).Fd()), p.Filename, &st, unix.AT_SYMLINK_NOFOLLOW)
			if !errors.Is(err, unix.ENOENT) {
				return errors.New("plugin destination is occupied")
			}
		}
	}
	for _, path := range []string{"s2script-plugins.lock.json", "egg/installed-list.json"} {
		dir, err := s2OpenDir(s.root, filepath.Dir(path), false)
		if err != nil {
			return err
		}
		err = s2CheckFile(dir, filepath.Base(path))
		dir.Close()
		if err != nil {
			return err
		}
	}
	type undo struct {
		p      s2Installed
		data   []byte
		exists bool
	}
	journal := []undo{}
	rollback := func() {
		for i := len(journal) - 1; i >= 0; i-- {
			u := journal[i]
			if u.exists {
				s2WriteAt(s.dir(u.p.Enabled), u.p.Filename, u.data)
			} else {
				unix.Unlinkat(int(s.dir(u.p.Enabled).Fd()), u.p.Filename, 0)
			}
		}
	}
	for _, p := range after {
		if p.LastCheckStatus == "missing" {
			continue
		}
		old, exists := oldByName[p.Name]
		data, downloaded := payloads[p.Name]
		if !downloaded && exists && old.Path == p.Path {
			continue
		}
		if !downloaded {
			var err error
			data, err = s2ReadAt(s.dir(old.Enabled), old.Filename, s2MaxPlugin)
			if err != nil {
				rollback()
				return err
			}
		}
		previous, err := s2ReadAt(s.dir(p.Enabled), p.Filename, s2MaxPlugin)
		present := err == nil
		if err != nil && !errors.Is(err, unix.ENOENT) {
			rollback()
			return err
		}
		journal = append(journal, undo{p, previous, present})
		if err = s2WriteAt(s.dir(p.Enabled), p.Filename, data); err != nil {
			rollback()
			return err
		}
		if exists && old.LastCheckStatus != "missing" && old.Path != p.Path {
			oldData, err := s2ReadAt(s.dir(old.Enabled), old.Filename, s2MaxPlugin)
			if err != nil {
				rollback()
				return err
			}
			journal = append(journal, undo{old, oldData, true})
			if err = unix.Unlinkat(int(s.dir(old.Enabled).Fd()), old.Filename, 0); err != nil {
				rollback()
				return err
			}
		}
	}
	remaining := map[string]bool{}
	for _, p := range after {
		remaining[p.Name] = true
	}
	for _, p := range before {
		if remaining[p.Name] || p.LastCheckStatus == "missing" {
			continue
		}
		data, err := s2ReadAt(s.dir(p.Enabled), p.Filename, s2MaxPlugin)
		if err != nil {
			rollback()
			return err
		}
		journal = append(journal, undo{p, data, true})
		if err = unix.Unlinkat(int(s.dir(p.Enabled).Fd()), p.Filename, 0); err != nil {
			rollback()
			return err
		}
	}
	if err := s.save(after); err != nil {
		rollback()
		return err
	}
	return nil
}

func (s *s2PluginStore) commitPolicy(path string, policy s2Policy, before, after []s2Installed) error {
	dir, err := s2OpenDir(s.root, filepath.Dir(path), true)
	if err != nil {
		return err
	}
	defer dir.Close()
	old, err := s2ReadAt(dir, filepath.Base(path), 4<<20)
	missing := errors.Is(err, unix.ENOENT)
	if err != nil && !missing {
		return err
	}
	if err = s.writeJSON(path, policy); err != nil {
		return err
	}
	if err = s.s2Apply(before, after, nil); err != nil {
		if missing {
			unix.Unlinkat(int(dir.Fd()), filepath.Base(path), 0)
		} else {
			s2WriteAt(dir, filepath.Base(path), old)
		}
		return err
	}
	return nil
}

func (s *s2PluginStore) forgetRoots(name string) {
	retained := []s2RegistryRoot{}
	for _, root := range s.roots {
		affected := root.Name == name
		for _, member := range root.Members {
			affected = affected || member.Name == name
		}
		if !affected {
			retained = append(retained, root)
		}
	}
	s.roots = retained
}

func s2ForgetParent(all []s2Installed, name string) []s2Installed {
	updated := append([]s2Installed(nil), all...)
	for i, p := range updated {
		parents := []string{}
		for _, parent := range p.Parents {
			if parent != name {
				parents = append(parents, parent)
			}
		}
		updated[i].Parents = parents
	}
	return updated
}

// CheckS2ScriptPlugins reconciles policy and checks the complete installed list
// during startup. The entrypoint already owns AcquireS2ScriptRuntime.
func (e *Env) CheckS2ScriptPlugins(ctx context.Context, getenv func(string) string) error {
	return e.s2PluginCommand(ctx, []string{"update"}, getenv, io.Discard, true)
}
func (e *Env) S2ScriptPluginCommand(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	return e.s2PluginCommand(ctx, args, getenv, out, false)
}
func (e *Env) s2PluginCommand(ctx context.Context, args []string, getenv func(string) string, out io.Writer, startup bool) error {
	if out == nil {
		out = io.Discard
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	if len(args) == 0 {
		return errors.New("usage: plugins list|inspect|install|add|update|check|enable|disable|remove|pin|unpin|reconcile [NAME] [--version SEMVER] [--disabled] [--with NAME,...]")
	}
	command := args[0]
	args = args[1:]
	name := ""
	switch command {
	case "list", "update", "check", "reconcile":
	case "inspect", "install", "add", "enable", "disable", "remove", "pin", "unpin":
		if len(args) == 0 || !s2PluginName.MatchString(args[0]) {
			return errors.New("plugin command needs a valid manifest identity")
		}
		name = args[0]
		args = args[1:]
	default:
		return errors.New("unknown plugin command")
	}
	flags := flag.NewFlagSet("plugins", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", e.Root, "server root")
	version := flags.String("version", "*", "version/range")
	disabled := flags.Bool("disabled", false, "install disabled")
	enabled := flags.Bool("enabled", false, "install enabled")
	with := flags.String("with", "", "optional identities")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (*disabled && *enabled) {
		return errors.New("invalid plugin command arguments")
	}
	invalid := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name != "root" && !((command == "install" || command == "add") && (f.Name == "version" || f.Name == "disabled" || f.Name == "enabled" || f.Name == "with")) && !(command == "pin" && f.Name == "version") {
			invalid = true
		}
	})
	if invalid {
		return errors.New("unsupported option for plugin command")
	}
	if command == "pin" && !s2ExactVersion(*version) {
		return errors.New("pin requires an exact SemVer --version")
	}
	selector, err := s2NormalizeVersion(*version)
	if err != nil {
		return err
	}
	panel, err := s2Panel(getenv)
	if err != nil {
		return err
	}
	policyPath, err := s2PolicyPath(getenv)
	if err != nil {
		return err
	}
	mutation := command != "list" && command != "inspect" && command != "check"
	if mutation && !startup {
		release, err := AcquireS2ScriptRuntime(*root)
		if err != nil {
			return err
		}
		defer release()
	}
	s, err := s2OpenStore(*root)
	if err != nil {
		return err
	}
	defer s.close()
	policy, err := s.policy(policyPath)
	if err != nil {
		return err
	}
	all, err := s.discover()
	if err != nil {
		return err
	}
	all, err = s.retained(all)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if command == "list" {
		return enc.Encode(struct {
			Policy    s2Policy      `json:"policy"`
			Installed []s2Installed `json:"installed"`
		}{policy, all})
	}
	index := -1
	for i, p := range all {
		if p.Name == name {
			index = i
			break
		}
	}
	entry, desired := policy.Plugins[name]
	if !desired {
		entry = s2PolicyEntry{Source: "registry", Version: "*"}
		if index >= 0 {
			entry.Source = all[index].Source
		}
	}
	if command == "inspect" {
		if index < 0 && !desired {
			return errors.New("plugin is not desired or installed")
		}
		return enc.Encode(struct {
			Desired   s2PolicyEntry `json:"desired"`
			Installed *s2Installed  `json:"installed,omitempty"`
		}{entry, func() *s2Installed {
			if index >= 0 {
				return &all[index]
			}
			return nil
		}()})
	}
	switch command {
	case "install", "add":
		entry.Source = "registry"
		entry.Version = selector
		if *with != "" {
			entry.With = strings.Split(*with, ",")
			seen := map[string]bool{}
			for _, dep := range entry.With {
				if !s2PluginName.MatchString(dep) || seen[dep] {
					return errors.New("invalid optional plugin identity")
				}
				seen[dep] = true
			}
		}
		if *enabled || *disabled {
			value := !*disabled
			entry.Enabled = &value
		}
		policy.Plugins[name] = entry
		if err = s.writeJSON(policyPath, policy); err != nil {
			return err
		}
	case "pin", "unpin":
		if index < 0 && !desired {
			return errors.New("plugin is not desired or installed")
		}
		entry.Version = "*"
		if command == "pin" {
			entry.Version = *version
		}
		policy.Plugins[name] = entry
		return s.writeJSON(policyPath, policy)
	case "enable", "disable", "remove":
		if command != "remove" && (index < 0 && desired || index >= 0 && all[index].LastCheckStatus == "missing") {
			value := command == "enable"
			entry.Enabled = &value
			policy.Plugins[name] = entry
			return s.commitPolicy(policyPath, policy, all, all)
		}
		if command == "remove" && index < 0 && desired && entry.Source == "registry" {
			delete(policy.Plugins, name)
			s.forgetRoots(name)
			return s.commitPolicy(policyPath, policy, all, s2ForgetParent(all, name))
		}
		if index < 0 || all[index].LastCheckStatus == "missing" && command != "remove" {
			return errors.New("plugin is not installed")
		}
		candidate := all[index]
		graph, err := s2Dependencies(all, s.hostVerified())
		if err != nil {
			return err
		}
		if command == "remove" || command == "disable" {
			for _, consumer := range all {
				if consumer.Name == name || command == "disable" && !consumer.Enabled {
					continue
				}
				for _, dep := range graph[consumer.Name] {
					if dep == name {
						return fmt.Errorf("plugin is still needed by %s", consumer.Name)
					}
				}
			}
		}
		if command == "remove" {
			if !candidate.Managed {
				return errors.New("refusing to remove unowned local plugin")
			}
			for _, parent := range candidate.Parents {
				if parent != name {
					if _, desired := policy.Plugins[parent]; desired {
						return fmt.Errorf("plugin is still needed by desired registry root %s", parent)
					}
				}
			}
			updated := append([]s2Installed(nil), all[:index]...)
			updated = append(updated, all[index+1:]...)
			updated = s2ForgetParent(updated, name)
			delete(policy.Plugins, name)
			s.forgetRoots(name)
			return s.commitPolicy(policyPath, policy, all, updated)
		}
		value := command == "enable"
		entry.Enabled = &value
		policy.Plugins[name] = entry
		updated := append([]s2Installed(nil), all...)
		updated[index].Enabled = value
		updated[index].Path = s2Target(candidate.Filename, value)
		if err = s2ValidateEnabled(updated, graph); err != nil {
			return err
		}
		return s.commitPolicy(policyPath, policy, all, updated)
	}
	// Caller root overrides are local to this operation.
	clone := *e
	clone.Root = *root
	all, checks, err := clone.s2Reconcile(ctx, s, policy, all, panel, getenv, command != "check")
	if err != nil {
		return err
	}
	_ = all
	return enc.Encode(checks)
}

// Policy selectors retain exact strings, including prerelease/build pins.
var s2VersionRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

func s2ExactVersion(v string) bool {
	m := s2VersionRE.FindStringSubmatch(v)
	if m == nil || len(v) > 128 {
		return false
	}
	for _, part := range strings.Split(m[4], ".") {
		if len(part) > 1 && part[0] == '0' && strings.Trim(part, "0123456789") == "" {
			return false
		}
	}
	return true
}
func s2NormalizeVersion(v string) (string, error) {
	if v == "" {
		return "*", nil
	}
	if v == "*" || s2ExactVersion(v) {
		return v, nil
	}
	if len(v) > 256 || strings.TrimSpace(v) != v || strings.ContainsAny(v, "\r\n\t\x00") {
		return "", errors.New("unsupported version range")
	}
	for _, group := range strings.Split(v, "||") {
		tokens := strings.Fields(group)
		if len(tokens) == 0 {
			return "", errors.New("empty version range")
		}
		for _, token := range tokens {
			base := strings.TrimLeft(token, "<>=^~")
			op := strings.TrimSuffix(token, base)
			if (op != "<" && op != ">" && op != "<=" && op != ">=" && op != "=" && op != "^" && op != "~") || !s2ExactVersion(base) {
				return "", errors.New("version must be *, exact SemVer, or a supported SemVer range")
			}
		}
	}
	return v, nil
}
func s2Compare(a, b string) int {
	split := func(v string) ([]string, []string) {
		parts := strings.SplitN(strings.SplitN(v, "+", 2)[0], "-", 2)
		var pre []string
		if len(parts) > 1 {
			pre = strings.Split(parts[1], ".")
		}
		return strings.Split(parts[0], "."), pre
	}
	an, ap := split(a)
	bn, bp := split(b)
	number := func(x, y string) int {
		if len(x) < len(y) {
			return -1
		}
		if len(x) > len(y) {
			return 1
		}
		return strings.Compare(x, y)
	}
	for i := 0; i < 3; i++ {
		if c := number(an[i], bn[i]); c != 0 {
			return c
		}
	}
	if len(ap) == 0 && len(bp) > 0 {
		return 1
	}
	if len(bp) == 0 && len(ap) > 0 {
		return -1
	}
	for i := 0; i < len(ap) && i < len(bp); i++ {
		x, y := ap[i], bp[i]
		xn, yn := strings.Trim(x, "0123456789") == "", strings.Trim(y, "0123456789") == ""
		c := strings.Compare(x, y)
		if xn && yn {
			c = number(x, y)
		} else if xn {
			c = -1
		} else if yn {
			c = 1
		}
		if c != 0 {
			return c
		}
	}
	if len(ap) < len(bp) {
		return -1
	}
	if len(ap) > len(bp) {
		return 1
	}
	return 0
}
func s2Satisfies(v, selector string) bool {
	if !s2ExactVersion(v) {
		return false
	}
	selector, err := s2NormalizeVersion(selector)
	if err != nil {
		return false
	}
	if s2ExactVersion(selector) {
		return v == selector
	}
	core := strings.SplitN(strings.SplitN(v, "+", 2)[0], "-", 2)[0]
	pre := strings.Contains(strings.SplitN(v, "+", 2)[0], "-")
	if selector == "*" {
		return !pre
	}
	for _, group := range strings.Split(selector, "||") {
		ok, allowsPre := true, !pre
		for _, token := range strings.Fields(group) {
			base := strings.TrimLeft(token, "<>=^~")
			op := strings.TrimSuffix(token, base)
			c := s2Compare(v, base)
			bc := strings.SplitN(strings.SplitN(base, "+", 2)[0], "-", 2)[0]
			if strings.Contains(strings.SplitN(base, "+", 2)[0], "-") && bc == core {
				allowsPre = true
			}
			switch op {
			case "=":
				ok = ok && c == 0
			case ">":
				ok = ok && c > 0
			case ">=":
				ok = ok && c >= 0
			case "<":
				ok = ok && c < 0
			case "<=":
				ok = ok && c <= 0
			case "^", "~":
				vp, bp := strings.Split(core, "."), strings.Split(bc, ".")
				same := vp[0] == bp[0]
				if op == "~" || bp[0] == "0" {
					same = same && vp[1] == bp[1]
				}
				if op == "^" && bp[0] == "0" && bp[1] == "0" {
					same = same && vp[2] == bp[2]
				}
				ok = ok && c >= 0 && same
			default:
				ok = false
			}
		}
		if ok && allowsPre {
			return true
		}
	}
	return false
}
