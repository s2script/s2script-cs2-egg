// SPDX-License-Identifier: GPL-3.0-or-later
package scheduler

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"io"
	"os"
	"regexp"
	"sync"
	"syscall"
	"time"
)

type Power interface {
	Restart(context.Context, string) error
	Stop(context.Context, string) error
	Start(context.Context, string) error
	State(context.Context, string) (string, error)
	Warning(context.Context, string, int) error
	RequestScheduledStop(context.Context, string, string) error
}
type Target struct {
	Generation string `json:"generation"`
	Build      string `json:"build"`
}
type Job struct {
	ID                string            `json:"id"`
	Server            string            `json:"server"`
	Target            Target            `json:"target"`
	SourceGeneration  string            `json:"source_generation"`
	OriginalRun       string            `json:"original_run"`
	AttachMode        string            `json:"attach_mode,omitempty"`
	Policy            Policy            `json:"policy"`
	PendingSince      time.Time         `json:"pending_since"`
	Deadline          time.Time         `json:"deadline"`
	ExecuteAt         time.Time         `json:"execute_at,omitempty"`
	Phase             string            `json:"phase"`
	ActionID          string            `json:"action_id,omitempty"`
	ActionAt          time.Time         `json:"action_at,omitempty"`
	ExpiresAt         time.Time         `json:"expires_at,omitempty"`
	Warnings          map[string]string `json:"warnings"`
	Reason            string            `json:"reason,omitempty"`
	AllowOfflineStart bool              `json:"allow_offline_start,omitempty"`
}
type Options struct {
	Directory string
	Power     Power
	Status    func(string) server.Status
	Pin       func(context.Context, string, string) error
	Select    func(context.Context, string, string) error
	Clear     func(context.Context, string, string) error
	Attach    func(context.Context, string, string, string) error
}
type Engine struct {
	mu        sync.Mutex
	o         Options
	jobs      []Job
	release   func() error
	recovered map[string]bool
}

var tokenPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var serverPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

type snapshot struct {
	Version int   `json:"version"`
	Jobs    []Job `json:"jobs"`
}

var buildPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

func token() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func Open(o Options) (*Engine, error) {
	if o.Status == nil || o.Pin == nil || o.Select == nil || o.Clear == nil {
		return nil, errors.New("scheduler needs host status/pin/target adapters")
	}
	if e := os.MkdirAll(o.Directory, 0700); e != nil {
		return nil, e
	}
	u, e := artifact.Acquire(o.Directory)
	if e != nil {
		return nil, e
	}
	engine := &Engine{o: o, release: u, recovered: map[string]bool{}}
	d, e := artifact.OpenDirectory(o.Directory)
	if e != nil {
		u()
		return nil, e
	}
	defer d.Close()
	f, e := artifact.OpenRegular(d, "jobs.json", syscall.O_RDONLY, 0)
	if errors.Is(e, os.ErrNotExist) {
		return engine, nil
	}
	if e != nil {
		u()
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	var saved snapshot
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if e != nil || len(b) > 4<<20 || decoder.Decode(&saved) != nil || decoder.Decode(new(any)) != io.EOF || saved.Version != 1 || len(saved.Jobs) > 4096 {
		u()
		return nil, errors.New("invalid bounded scheduler snapshot")
	}
	engine.jobs = saved.Jobs
	ids := map[string]bool{}
	active := map[string]bool{}
	keys := map[string]bool{}
	for _, j := range engine.jobs {
		key := j.Server + ":" + j.Target.Generation
		phases := map[string]bool{"finishing": true, "canceling": true, "waiting": true, "arming": true, "stop_requested": true, "offline_requested": true, "attach_pending": true, "attached": true, "restart_requested": true, "start_requested": true, "hold": true, "done": true, "canceled": true}
		terminal := j.Phase == "done" || j.Phase == "canceled"
		if !phases[j.Phase] || !serverPattern.MatchString(j.Server) || keys[key] || (!terminal && active[j.Server]) || j.PendingSince.IsZero() || !j.Deadline.Equal(j.PendingSince.Add(j.Policy.MaximumDelay)) || len(j.OriginalRun) > 128 || len(j.Reason) > 256 || j.Warnings == nil || len(j.Warnings) > 16 || (j.ActionID != "" && !tokenPattern.MatchString(j.ActionID)) || (j.AttachMode != "" && j.AttachMode != "copy" && j.AttachMode != "hardlink") || !tokenPattern.MatchString(j.ID) || ids[j.ID] || !tokenPattern.MatchString(j.Target.Generation) || !buildPattern.MatchString(j.Target.Build) || j.Policy.Validate() != nil {
			u()
			return nil, errors.New("invalid persisted job")
		}
		ids[j.ID] = true
		keys[key] = true
		if !terminal {
			active[j.Server] = true
		}
		if j.Phase != "waiting" && j.Phase != "done" && j.Phase != "canceled" && j.Phase != "finishing" && j.Phase != "canceling" {
			engine.recovered[j.ID] = true
		}
	}
	return engine, nil
}
func (e *Engine) Close() error { e.mu.Lock(); defer e.mu.Unlock(); return e.release() }
func (e *Engine) save() error {
	b, err := json.Marshal(snapshot{Version: 1, Jobs: e.jobs})
	if err != nil || len(b) > 4<<20 {
		return errors.New("scheduler snapshot limit")
	}
	d, err := artifact.OpenDirectory(e.o.Directory)
	if err != nil {
		return err
	}
	defer d.Close()
	name := "jobs.tmp-" + token()
	f, err := artifact.OpenRegular(d, name, syscall.O_CREAT|syscall.O_EXCL|syscall.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer artifact.Remove(d, name)
	_, err = f.Write(b)
	err = errors.Join(err, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	return artifact.Rename(d, name, "jobs.json")
}
func (e *Engine) Jobs() []Job {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, _ := json.Marshal(e.jobs)
	var out []Job
	json.Unmarshal(b, &out)
	return out
}
func (e *Engine) Schedule(ctx context.Context, id string, target Target, mode string, p Policy, s server.Status, now time.Time) (Job, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !serverPattern.MatchString(id) || now.IsZero() || p.Validate() != nil || !tokenPattern.MatchString(target.Generation) || !buildPattern.MatchString(target.Build) || (mode != "" && mode != "hardlink" && mode != "copy") {
		return Job{}, errors.New("invalid generation/restart policy")
	}
	for _, j := range e.jobs {
		if j.Server == id && j.Target.Generation == target.Generation {
			return j, nil
		}
		if j.Server == id && j.Phase != "done" && j.Phase != "canceled" {
			return Job{}, errors.New("server already has an active update job")
		}
	}
	if len(e.jobs) >= 4096 {
		return Job{}, errors.New("scheduler job quota exhausted")
	}
	if err := e.o.Pin(ctx, id, target.Generation); err != nil {
		return Job{}, err
	}
	j := Job{ID: token(), Server: id, Target: target, SourceGeneration: s.Versions["shared_generation"], OriginalRun: s.RunID, AttachMode: mode, Policy: p, PendingSince: now.UTC(), Deadline: now.Add(p.MaximumDelay).UTC(), Phase: "waiting", Warnings: map[string]string{}}
	if s.RunID == "" || s.State == "stopped" || s.OperatorVeto || s.IntendedStopReason != "" {
		j.Phase = "hold"
		j.Reason = "offline_or_operator_stop"
	}
	e.jobs = append(e.jobs, j)
	if err := e.save(); err != nil {
		e.jobs = e.jobs[:len(e.jobs)-1]
		return Job{}, err
	}
	return j, nil
}
func (e *Engine) hold(j *Job, reason string) error {
	j.Phase = "hold"
	j.Reason = reason
	return e.save()
}

// elapsedClock preserves the dispatcher's clock domain while including time spent
// waiting for the engine lock, Wings, warnings, and the shared store.
func elapsedClock(now time.Time) func() time.Time {
	started := time.Now()
	return func() time.Time { return now.Add(time.Since(started)).UTC() }
}
func (e *Engine) observe(ctx context.Context, j *Job, clock func() time.Time) (string, server.Status, time.Time, error) {
	state, err := e.o.Power.State(ctx, j.Server)
	// Read runtime evidence and time AFTER the potentially blocking HTTP call.
	s := e.o.Status(j.Server)
	return state, s, clock(), err
}
func correlated(j *Job, s server.Status) bool {
	return s.RunID == j.OriginalRun && s.State == "stopped" && s.IntendedStopReason == "scheduled_update" && s.ScheduledActionID == j.ActionID
}
func (e *Engine) updateReady(j *Job, s server.Status, state string, now time.Time) (bool, error) {
	if s.OperatorVeto || s.RunID != j.OriginalRun || s.RunID == "" || s.IntendedStopReason != "" || s.State == "stopped" || state == "offline" {
		return false, e.hold(j, "original_run_stopped_or_changed")
	}
	if state != "running" || !Fresh(now, s, j.Policy.Freshness) {
		return false, nil
	}
	if !Eligible(now, j.PendingSince, s, j.Policy) {
		j.Phase = "waiting"
		j.ExecuteAt = time.Time{}
		j.ActionID = ""
		j.ExpiresAt = time.Time{}
		return false, e.save()
	}
	return true, nil
}

// dispatchReady runs after durable intent and all other blocking preparation.
// Wings offers no atomic observe-and-act primitive: a later remote stop remains
// a race, but an already observable stop must never be overridden.
func (e *Engine) dispatchReady(ctx context.Context, j *Job, previous string, clock func() time.Time) (bool, error) {
	state, s, now, err := e.observe(ctx, j, clock)
	if err != nil {
		return false, err
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	if s.OperatorVeto && !(j.AllowOfflineStart && state == "offline") {
		return false, e.hold(j, "operator_veto")
	}
	switch previous {
	case "waiting", "arming":
		if ok, err := e.updateReady(j, s, state, now); !ok {
			return false, err
		}
		if previous == "arming" && (!now.Before(j.ExpiresAt) || s.ScheduledActionID != j.ActionID) {
			return false, e.hold(j, "stop_arm_expired_or_run_changed")
		}
		return !now.Before(j.ExecuteAt), nil
	case "stop_requested":
		if !correlated(j, s) {
			return false, e.hold(j, "missing_correlated_termination")
		}
		return state != "offline", nil
	case "attached":
		if state != "offline" || (!j.AllowOfflineStart && !correlated(j, s)) {
			return false, e.hold(j, "state_changed_after_attach")
		}
		return true, nil
	}
	return false, errors.New("invalid dispatch phase")
}
func (e *Engine) action(ctx context.Context, j *Job, phase string, clock func() time.Time, call func() error) error {
	previous, previousAction := j.Phase, j.ActionAt
	j.Phase = phase
	j.ActionAt = clock()
	if err := e.save(); err != nil {
		return err
	}
	if ok, err := e.dispatchReady(ctx, j, previous, clock); !ok {
		if j.Phase == phase {
			j.Phase = previous
		}
		j.ActionAt = previousAction
		return errors.Join(err, e.save())
	}
	if err := call(); err != nil {
		j.Reason = "action_outcome_ambiguous"
		return errors.Join(err, e.save())
	}
	j.Reason = "accepted_awaiting_lifecycle"
	return e.save()
}

// Step is invoked only by the host action dispatcher. API acceptance is never completion.
func (e *Engine) Step(ctx context.Context, id string, now time.Time) error {
	clock := elapsedClock(now)
	e.mu.Lock()
	defer e.mu.Unlock()
	var j *Job
	for i := range e.jobs {
		if e.jobs[i].ID == id {
			j = &e.jobs[i]
			break
		}
	}
	if j == nil {
		return errors.New("unknown update job")
	}
	if j.Phase == "done" || j.Phase == "canceled" {
		return nil
	}
	if j.Phase == "finishing" || j.Phase == "canceling" {
		return e.finish(ctx, j)
	}
	s := e.o.Status(j.Server)
	now = clock()
	if !j.ActionAt.IsZero() && s.RunID != "" && s.RunID != j.OriginalRun && s.Ready && Fresh(now, s, j.Policy.Freshness) && s.Versions["shared_generation"] == j.Target.Generation && s.Versions["steam_build"] == j.Target.Build && !s.OperatorVeto {
		j.Phase = "finishing"
		j.Reason = "target_ready"
		if err := e.save(); err != nil {
			return err
		}
		return e.finish(ctx, j)
	}
	if e.recovered[j.ID] {
		delete(e.recovered, j.ID)
		return e.hold(j, "recovered_action_requires_admin_resolution")
	}
	if j.Phase == "hold" {
		return nil
	}
	if e.o.Power == nil {
		return e.hold(j, "Wings_adapter_unavailable")
	}
	state, s, now, err := e.observe(ctx, j, clock)
	if err != nil {
		return err
	}
	if s.OperatorVeto && !(j.AllowOfflineStart && state == "offline") {
		return e.hold(j, "operator_veto")
	}
	switch j.Phase {
	case "waiting":
		if ok, err := e.updateReady(j, s, state, now); !ok {
			return err
		}
		if j.ExecuteAt.IsZero() {
			j.ExecuteAt = now.Add(Countdown(j.Policy))
			if j.ExecuteAt.After(j.Deadline) {
				j.ExecuteAt = j.Deadline
			}
			if err := e.save(); err != nil {
				return err
			}
		}
		for _, offset := range j.Policy.WarningOffsets {
			key := offset.String()
			if _, ok := j.Warnings[key]; ok || now.Before(j.ExecuteAt.Add(-offset)) {
				continue
			}
			j.Warnings[key] = "attempted"
			if err := e.save(); err != nil {
				return err
			}
			seconds := int(j.ExecuteAt.Sub(now).Seconds())
			if seconds < 0 {
				seconds = 0
			}
			err := e.o.Power.Warning(ctx, j.Server, seconds)
			j.Warnings[key] = "dispatch_accepted"
			if err != nil {
				j.Warnings[key] = "failed_or_ambiguous"
			}
			if err := e.save(); err != nil {
				return err
			}
			state, s, now, err = e.observe(ctx, j, clock)
			if err != nil {
				return err
			}
			if ok, err := e.updateReady(j, s, state, now); !ok {
				return err
			}
		}
		now = clock()
		if now.Before(j.ExecuteAt) {
			return nil
		}
		if err := e.o.Select(ctx, j.Server, j.Target.Generation); err != nil {
			return err
		}
		if j.AttachMode == "" {
			return e.action(ctx, j, "restart_requested", clock, func() error { return e.o.Power.Restart(ctx, j.Server) })
		}
		state, s, now, err = e.observe(ctx, j, clock)
		if err != nil {
			return err
		}
		if ok, err := e.updateReady(j, s, state, now); !ok {
			return err
		}
		j.ActionID = token()
		j.ExpiresAt = now.Add(30 * time.Second)
		j.Phase = "arming"
		return e.save()
	case "arming":
		if !Eligible(now, j.PendingSince, s, j.Policy) {
			j.Phase = "waiting"
			j.ExecuteAt = time.Time{}
			j.ActionID = ""
			j.ExpiresAt = time.Time{}
			return e.save()
		}
		if s.RunID != j.OriginalRun || !Fresh(now, s, j.Policy.Freshness) || state != "running" || s.IntendedStopReason != "" || !now.Before(j.ExpiresAt) {
			return e.hold(j, "stop_arm_expired_or_run_changed")
		}
		if s.ScheduledActionID != j.ActionID {
			return nil
		}
		return e.action(ctx, j, "stop_requested", clock, func() error { return e.o.Power.RequestScheduledStop(ctx, j.Server, j.ActionID) })
	case "stop_requested":
		if s.RunID != j.OriginalRun || s.State != "stopped" || s.IntendedStopReason != "scheduled_update" || s.ScheduledActionID != j.ActionID {
			return nil
		}
		if state != "offline" {
			return e.action(ctx, j, "offline_requested", clock, func() error { return e.o.Power.Stop(ctx, j.Server) })
		}
		j.Phase = "attach_pending"
		return e.save()
	case "offline_requested":
		if s.RunID != j.OriginalRun || s.State != "stopped" || s.IntendedStopReason != "scheduled_update" || s.ScheduledActionID != j.ActionID {
			return e.hold(j, "missing_correlated_termination")
		}
		if state != "offline" {
			return nil
		}
		j.Phase = "attach_pending"
		return e.save()
	case "attach_pending":
		if state != "offline" {
			return e.hold(j, "not_offline_before_attach")
		}
		if !j.AllowOfflineStart && (s.RunID != j.OriginalRun || s.State != "stopped" || s.IntendedStopReason != "scheduled_update" || s.ScheduledActionID != j.ActionID) {
			return e.hold(j, "missing_correlated_termination")
		}
		if e.o.Attach == nil {
			return e.hold(j, "helper_unavailable")
		}
		if err := e.o.Attach(ctx, j.Server, j.Target.Generation, j.AttachMode); err != nil {
			return err
		}
		j.Phase = "attached"
		return e.save()
	case "attached":
		if state != "offline" {
			return e.hold(j, "state_changed_after_attach")
		}
		return e.action(ctx, j, "start_requested", clock, func() error { return e.o.Power.Start(ctx, j.Server) })
	case "restart_requested", "start_requested":
		return nil
	}
	return errors.New("unknown job phase")
}

// Resolve is host-administrator only. Offline starts require explicit authorization.
func (e *Engine) Resolve(ctx context.Context, id, operation string, allowStart bool, now time.Time) error {
	clock := elapsedClock(now)
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.jobs {
		j := &e.jobs[i]
		if j.ID != id {
			continue
		}
		if operation == "cancel" && (j.Phase == "done" || j.Phase == "canceled") {
			return nil
		}
		if operation == "cancel" {
			j.Phase = "canceling"
			j.Reason = "administrator_cancel"
			delete(e.recovered, id)
			if err := e.save(); err != nil {
				return err
			}
			return e.finish(ctx, j)
		}
		if operation != "retry" || j.Phase == "done" || j.Phase == "canceled" {
			return errors.New("invalid administrator operation")
		}
		if e.o.Power == nil {
			return errors.New("Wings unavailable")
		}
		state, err := e.o.Power.State(ctx, j.Server)
		if err != nil {
			return err
		}
		s := e.o.Status(j.Server)
		now = clock()
		if state == "offline" {
			if !allowStart {
				return errors.New("offline retry requires explicit allow-start")
			}
			j.AllowOfflineStart = true
			if j.AttachMode != "" {
				j.Phase = "attach_pending"
			} else {
				j.Phase = "attached"
			}
		} else {
			if state != "running" || !Fresh(now, s, j.Policy.Freshness) || s.OperatorVeto || s.IntendedStopReason != "" {
				return errors.New("fresh active run required for retry")
			}
			j.OriginalRun = s.RunID
			j.Phase = "waiting"
			j.ExecuteAt = time.Time{}
			j.ActionAt = time.Time{}
			j.ActionID = ""
		}
		delete(e.recovered, id)
		if err := e.o.Select(ctx, j.Server, j.Target.Generation); err != nil {
			return err
		}
		return e.save()
	}
	return errors.New("unknown update job")
}

// Selection/pin cleanup is itself retryable; it must precede terminal dedup state.
func (e *Engine) finish(ctx context.Context, j *Job) error {
	if err := e.o.Clear(ctx, j.Server, j.Target.Generation); err != nil {
		return err
	}
	if j.Phase == "canceling" {
		j.Phase = "canceled"
	} else {
		j.Phase = "done"
	}
	return e.save()
}
