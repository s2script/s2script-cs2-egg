// SPDX-License-Identifier: GPL-3.0-or-later
package scheduler

import (
	"context"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"sync"
	"time"
)

type ServerPolicy struct {
	Policy     Policy
	AttachMode string
}
type Module struct {
	Engine    *Engine
	Policies  map[string]ServerPolicy
	Poll      func(context.Context) (Target, error)
	Interval  time.Duration
	Clock     Clock
	host      nodetypes.Host
	cancel    context.CancelFunc
	done      chan struct{}
	mu        sync.Mutex
	lastError string
}

func (m *Module) Name() string { return "updates" }
func (m *Module) Capabilities() []string {
	return []string{protocol.CapRestart, protocol.CapUpdateControl, protocol.CapUpdateExecute, protocol.CapUpdateAdmin}
}
func (m *Module) Start(ctx context.Context, h nodetypes.Host) error {
	if m.Engine == nil || m.Poll == nil || m.Interval < time.Second {
		return errors.New("invalid updates module")
	}
	m.host = h
	if m.Clock == nil {
		m.Clock = RealClock{}
	}
	ctx, m.cancel = context.WithCancel(ctx)
	m.done = make(chan struct{})

	go func() {
		defer close(m.done)
		var workers sync.WaitGroup
		workers.Add(1)
		go func() {
			defer workers.Done()
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
					pollCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
					target, err := m.Poll(pollCtx)
					cancel()
					if err != nil {
						m.record(err)
					} else {
						now := m.Clock.Now()
						for _, srv := range h.Servers() {
							if p, ok := m.Policies[srv.ID]; ok {
								status := h.Status(srv.ID)
								if status.Versions["shared_generation"] == target.Generation {
									continue
								}
								if _, err := m.Engine.Schedule(ctx, srv.ID, target, p.AttachMode, p.Policy, status, now); err != nil {
									m.record(err)
								}
							}
						}
					}
					timer.Reset(m.Interval)
				}
			}
		}()
		defer workers.Wait()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				for _, job := range m.Engine.Jobs() {
					if job.Phase == "done" || job.Phase == "canceled" {
						continue
					}
					actionCtx, cancel := context.WithTimeout(ctx, 35*time.Minute)
					_, err := h.Dispatch(actionCtx, job.Server, protocol.CapUpdateExecute, protocol.ExecuteUpdate{JobID: job.ID})
					cancel()
					if err != nil {
						m.record(err)
					}
				}
			}
		}
	}()

	return nil
}
func (m *Module) record(e error) { m.mu.Lock(); m.lastError = e.Error(); m.mu.Unlock() }
func (m *Module) Health() nodetypes.Health {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastError != "" {
		return nodetypes.Health{Status: "degraded", Explanation: m.lastError}
	}
	return nodetypes.Health{Status: "healthy"}
}
func (m *Module) Close(ctx context.Context) error {
	if m.cancel != nil {
		m.cancel()
	}
	if m.done != nil {
		select {
		case <-m.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return m.Engine.Close()
}
func (m *Module) Handle(ctx context.Context, s nodetypes.Server, cap string, msg protocol.Message) (any, error) {
	now := m.Clock.Now()
	switch cap {
	case protocol.CapUpdateExecute:
		var r protocol.ExecuteUpdate
		if protocol.Decode(msg.Payload, &r) != nil {
			return nil, errors.New("invalid executor request")
		}
		for _, j := range m.Engine.Jobs() {
			if j.ID == r.JobID && j.Server == s.ID {
				return struct{}{}, m.Engine.Step(ctx, j.ID, now)
			}
		}
		return nil, errors.New("job/server mismatch")
	case protocol.CapUpdateAdmin:
		var r protocol.UpdateAdmin
		if protocol.Decode(msg.Payload, &r) != nil {
			return nil, errors.New("invalid admin request")
		}
		if r.Operation == "list" {
			return m.Engine.Jobs(), nil
		}
		return struct{}{}, m.Engine.Resolve(ctx, r.JobID, r.Operation, r.AllowStart, now)
	case protocol.CapUpdateControl:
		var r protocol.ControlRequest
		if protocol.Decode(msg.Payload, &r) != nil {
			return nil, errors.New("invalid control request")
		}
		status := m.host.Status(s.ID)
		for _, j := range m.Engine.Jobs() {
			if j.Server == s.ID && j.Phase == "arming" && j.OriginalRun == r.RunID && status.RunID == r.RunID && !status.OperatorVeto && status.IntendedStopReason == "" && Fresh(now, status, j.Policy.Freshness) && now.Before(j.ExpiresAt) {
				return protocol.UpdateControl{ActionID: j.ActionID, RunID: r.RunID, Target: j.Target.Generation, ExpiresAt: j.ExpiresAt}, nil
			}
		}
		return protocol.UpdateControl{}, nil
	case protocol.CapRestart: // Admission/status only: policy and target remain administrator owned.
		for _, j := range m.Engine.Jobs() {
			if j.Server == s.ID && j.Phase != "done" && j.Phase != "canceled" {
				return protocol.RestartResult{ScheduleID: j.ID, Generation: j.Target.Generation, BuildID: j.Target.Build, State: j.Phase}, nil
			}
		}
		return nil, errors.New("no published pending update; host poller admits jobs")
	}
	return nil, errors.New("unsupported update operation")
}
