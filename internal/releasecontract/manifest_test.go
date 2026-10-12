// SPDX-License-Identifier: GPL-3.0-or-later
package releasecontract

import (
	"encoding/json"
	"github.com/s2script/s2script-cs2-egg/internal/schematest"
	"strings"
	"testing"
)

func fixture(t *testing.T) Manifest {
	t.Helper()
	m, err := New("1.2.3", strings.Repeat("a", 40), map[string]FileDigest{
		"s2node": {SHA256: strings.Repeat("b", 64), Size: 3}, "s2egg": {SHA256: strings.Repeat("c", 64), Size: 4}, "source": {SHA256: strings.Repeat("d", 64), Size: 5}, "egg": {SHA256: strings.Repeat("e", 64), Size: 6}, "deployment": {SHA256: strings.Repeat("f", 64), Size: 7}})
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func TestCanonicalSemVerOrdering(t *testing.T) {
	for _, v := range []string{"v1.2.3", "01.2.3", "1.2", "1.2.3+build", "1.2.3-01", "1.2.3-"} {
		if _, err := ParseVersion(v); err == nil {
			t.Fatal(v)
		}
	}
	seq := []string{"1.0.0-alpha", "1.0.0-alpha.2", "1.0.0-alpha.10", "1.0.0-beta", "1.0.0", "2.0.0"}
	for i := 1; i < len(seq); i++ {
		if Compare(seq[i-1], seq[i]) >= 0 {
			t.Fatal(seq)
		}
	}
}
func TestManifestStrictIdentityAndFields(t *testing.T) {
	m := fixture(t)
	b, _ := json.Marshal(m)
	if _, err := Decode(b); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(string(b), `"channel":"stable"`, `"channel":"prerelease"`, 1), strings.Replace(string(b), `"schema_version":1,`, "", 1), strings.Replace(string(b), `"schema_version":1`, `"schema_version":1,"unknown":1`, 1), strings.Replace(string(b), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1), strings.Replace(string(b), "github.com/s2script/", "github.com/foreign/", 1)} {
		if _, err := Decode([]byte(bad)); err == nil {
			t.Fatal("accepted invalid manifest", bad)
		}
	}
}
func TestProtocolMismatchPreventsActivation(t *testing.T) {
	old := fixture(t)
	candidate := fixture(t)
	candidate.Compatibility.NodeContainer.Protocol = Range{Min: 2, Max: 2}
	if Compatible(old, candidate, nil) == nil {
		t.Fatal("protocol mismatch accepted")
	}
	candidate = fixture(t)
	candidate.Compatibility.State.Write = 2
	if Compatible(old, candidate, nil) == nil {
		t.Fatal("rollback cannot read candidate state")
	}
}
func TestHelperAndRollbackCompatibilityAreIndependent(t *testing.T) {
	a := fixture(t)
	b := fixture(t)
	if err := Compatible(a, b, nil); err != nil {
		t.Fatal(err)
	}
	actual := Helper{DiscoveryVersion: 1, Protocol: Range{1, 1}, Provides: []string{"shared.attach.v1"}}
	if HelperCompatible(b.Compatibility.Helper, actual, []string{"backup.catalog"}) == nil {
		t.Fatal("wire1 assumed sufficient for backup")
	}
	b.Compatibility.LauncherMin = 2
	if Compatible(a, b, nil) == nil {
		t.Fatal("unknown launcher accepted")
	}
}
func TestManifestRejectsCaseAliasFields(t *testing.T) {
	m := fixture(t)
	b, _ := json.Marshal(m)
	b = []byte(strings.Replace(string(b), `"schema_version":1`, `"schema_version":1,"SCHEMA_VERSION":1`, 1))
	if _, err := Decode(b); err == nil {
		t.Fatal("case alias accepted as unknown field")
	}
}
func TestManifestMatchesPublishedSchema(t *testing.T) {
	schematest.Validate(t, "release-manifest.schema.json", fixture(t))
}
