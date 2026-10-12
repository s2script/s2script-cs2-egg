// SPDX-License-Identifier: GPL-3.0-or-later
package shared

import (
	"context"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/steam"
	"os"
	"path/filepath"
)

// PollUpdate shares the administrative Build lock, including metadata polling.
func (s *Store) PollUpdate(ctx context.Context, latest func(context.Context, string) (string, error)) (Generation, bool, error) {
	u, e := artifact.Acquire(filepath.Join(s.root, ".build"))
	if e != nil {
		return Generation{}, false, e
	}
	defer u()
	if latest == nil {
		latest = steam.LatestBuild
	}
	build, e := latest(ctx, s.options.SteamCMD)
	if e != nil {
		return Generation{}, false, e
	}
	current, e := s.Current()
	if e == nil && current.BuildID == build {
		return current, false, nil
	}
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return Generation{}, false, e
	}
	g, e := s.buildLocked(ctx)
	return g, e == nil, e
}
func (s *Store) targets() (map[string]string, error) { return s.generationMap("update-targets.json") }
func (s *Store) generationMap(name string) (map[string]string, error) {
	v := map[string]string{}
	e := readJSON(s.root, name, &v)
	if errors.Is(e, os.ErrNotExist) {
		return v, nil
	}
	if e != nil {
		return nil, e
	}
	if len(v) > 256 {
		return nil, errors.New("update target quota")
	}
	for id, g := range v {
		if id == "" || len(id) > 128 || !validID.MatchString(g) {
			return nil, errors.New("invalid update target")
		}
	}
	return v, nil
}

// PinTarget is host-only; container preparation never chooses a generation.
func (s *Store) PinTarget(ctx context.Context, id, generation string) error {
	unlock, e := artifact.Acquire(s.root)
	if e != nil {
		return e
	}
	defer unlock()
	if e = ctx.Err(); e != nil {
		return e
	}
	if id == "" || len(id) > 128 {
		return errors.New("invalid scheduler pin identity")
	}
	if _, e = LoadManifest(s.root, generation, ""); e != nil {
		return e
	}
	pins, e := s.generationMap("update-pins.json")
	if e != nil {
		return e
	}
	pins[id] = generation
	return writeJSON(s.root, "update-pins.json", pins, 0600)
}

// SelectTarget cannot perform unowned cleanup; use ClearTarget with the job's generation.
func (s *Store) SelectTarget(ctx context.Context, id, generation string) error {
	if e := s.PinTarget(ctx, id, generation); e != nil {
		return e
	}
	u, e := artifact.Acquire(s.root)
	if e != nil {
		return e
	}
	defer u()
	if e = ctx.Err(); e != nil {
		return e
	}
	v, e := s.targets()
	if e != nil {
		return e
	}
	v[id] = generation
	return writeJSON(s.root, "update-targets.json", v, 0600)
}

// ClearTarget fences each deletion to the generation owned by the finishing job.
// Generation is sufficient because scheduler history deduplicates server/generation
// pairs permanently. Both maps share the store lock; partial I/O failure is retryable.
func (s *Store) ClearTarget(ctx context.Context, id, generation string) error {
	if id == "" || len(id) > 128 || !validID.MatchString(generation) {
		return errors.New("invalid scheduler cleanup identity")
	}
	u, e := artifact.Acquire(s.root)
	if e != nil {
		return e
	}
	defer u()
	if e = ctx.Err(); e != nil {
		return e
	}
	v, e := s.targets()
	if e != nil {
		return e
	}
	pins, e := s.generationMap("update-pins.json")
	if e != nil {
		return e
	}
	if v[id] == generation {
		delete(v, id)
		if e = writeJSON(s.root, "update-targets.json", v, 0600); e != nil {
			return e
		}
	}
	if pins[id] == generation {
		delete(pins, id)
		return writeJSON(s.root, "update-pins.json", pins, 0600)
	}
	return nil
}
