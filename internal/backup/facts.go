// SPDX-License-Identifier: GPL-3.0-or-later
package backup

import (
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"os"
)

// Persisted shared selection is provenance plus a private-file selection input;
// its active attachment/control markers are never archive members.
func (s *Store) facts() error {
	s.overrides = nil
	s.versions = map[string]string{}
	b, e := storeio.Read(s.volume, ".s2shared.json", 16<<20)
	if e == nil {
		var facts struct {
			Overrides  []string `json:"overrides"`
			Generation struct {
				ID      string `json:"id"`
				BuildID string `json:"build_id"`
			} `json:"generation"`
		}
		if json.Unmarshal(b, &facts) != nil || len(facts.Overrides) > 128 {
			return errors.New("invalid persisted override facts")
		}
		for _, p := range facts.Overrides {
			if !relative(p) || reserved(p) {
				return errors.New("unsafe persisted private override")
			}
			s.overrides = append(s.overrides, p)
		}
		s.versions["shared_generation"] = facts.Generation.ID
		s.versions["cs2_build"] = facts.Generation.BuildID
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	b, e = storeio.Read(s.volume, "egg/framework-state.json", 4<<20)
	if e == nil {
		var f struct {
			S2Script struct {
				Version string `json:"version"`
			} `json:"s2script"`
			Metamod struct {
				Version string `json:"version"`
			} `json:"metamod"`
		}
		if json.Unmarshal(b, &f) != nil {
			return errors.New("invalid framework facts")
		}
		s.versions["s2script"] = f.S2Script.Version
		s.versions["metamod"] = f.Metamod.Version
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	for _, v := range s.versions {
		if len(v) > 256 {
			return errors.New("version fact length bound")
		}
	}
	return nil
}
