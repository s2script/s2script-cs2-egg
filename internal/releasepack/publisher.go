// SPDX-License-Identifier: GPL-3.0-or-later
package releasepack

import (
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/releasecontract"
	"strings"
)

type PublisherAsset struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

func VerifyPublished(files map[string]releasecontract.FileDigest, assets []PublisherAsset) error {
	seen := map[string]bool{}
	for _, a := range assets {
		if seen[a.Name] {
			return errors.New("duplicate publisher asset")
		}
		seen[a.Name] = true
		d, ok := files[a.Name]
		if !ok {
			continue
		}
		if a.State != "uploaded" || a.Size != d.Size || a.Digest != "sha256:"+d.SHA256 {
			return errors.New("publisher did not confirm matching uploaded digest/size")
		}
	}
	for name := range files {
		if !seen[name] {
			return errors.New("published release is incomplete")
		}
	}
	return nil
}

type ImageIdentity struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
	Digest   string `json:"digest"`
}

func (i ImageIdentity) Validate() error {
	if _, e := releasecontract.ParseVersion(i.Version); e != nil {
		return e
	}
	hex := func(s string, n int) bool {
		if len(s) != n {
			return false
		}
		for _, c := range s {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
		return true
	}
	if !hex(i.Revision, 40) || !strings.HasPrefix(i.Digest, "sha256:") || !hex(strings.TrimPrefix(i.Digest, "sha256:"), 64) {
		return errors.New("invalid image identity")
	}
	return nil
}
func Promote(channel string, current *ImageIdentity, candidate ImageIdentity) (bool, error) {
	if channel != "stable" && channel != "prerelease" {
		return false, errors.New("invalid image channel")
	}
	if e := candidate.Validate(); e != nil {
		return false, e
	}
	v, _ := releasecontract.ParseVersion(candidate.Version)
	if current != nil {
		if e := current.Validate(); e != nil {
			return false, e
		}
	}
	if channel == "stable" && len(v.Pre) > 0 {
		return false, nil
	}
	if current == nil {
		return true, nil
	}
	cmp := releasecontract.Compare(candidate.Version, current.Version)
	if cmp == 0 && (candidate.Revision != current.Revision || candidate.Digest != current.Digest) {
		return false, errors.New("equal version has conflicting image identity")
	}
	return cmp > 0, nil
}
