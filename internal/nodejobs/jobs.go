// SPDX-License-Identifier: GPL-3.0-or-later
// Package nodejobs moves bounded long operations outside the five-second wire call.
package nodejobs

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Job struct {
	ID, Server, State, Error string
	Input                    json.RawMessage
	Output                   json.RawMessage
}
type Pool struct {
	root   string
	mu     sync.Mutex
	jobs   map[string]Job
	queue  chan string
	cancel context.CancelFunc
	wg     sync.WaitGroup
	run    func(context.Context, string, json.RawMessage) (any, error)
}

func New(root string, run func(context.Context, string, json.RawMessage) (any, error)) (*Pool, error) {
	if e := os.MkdirAll(root, 0700); e != nil {
		return nil, e
	}
	return &Pool{root: root, jobs: map[string]Job{}, queue: make(chan string, 128), run: run}, nil
}
func (p *Pool) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		return errors.New("job pool already started")
	}
	ctx, p.cancel = context.WithCancel(ctx)
	files, e := os.ReadDir(p.root)
	if e != nil {
		return e
	}
	if len(files) > 1024 {
		return errors.New("job quota")
	}
	kept := make([]os.DirEntry, 0, len(files))
	for _, f := range files {
		if strings.HasPrefix(f.Name(), ".tmp-") && len(f.Name()) == 37 {
			continue
		}
		if len(f.Name()) != 69 || filepath.Ext(f.Name()) != ".json" {
			return errors.New("unexpected job record")
		}
		var j Job
		if e = storeio.JSON(p.root, f.Name(), &j); e != nil {
			return e
		}
		if !storeio.Digest(j.ID) || f.Name() != j.ID+".json" || j.Server == "" || len(j.Server) > 128 || len(j.Input) > 64<<10 || (j.State != "pending" && j.State != "running" && j.State != "ready" && j.State != "failed") {
			return errors.New("invalid job record")
		}
		p.jobs[j.ID] = j
		kept = append(kept, f)
	}
	files = kept
	for range 2 {
		p.wg.Add(1)
		go p.worker(ctx)
	}
	go func() {
		for _, f := range files {
			select {
			case p.queue <- f.Name()[:64]:
			case <-ctx.Done():
				return
			}
		}
	}()
	return nil
}
func (p *Pool) worker(ctx context.Context) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-p.queue:
			p.mu.Lock()
			j, ok := p.jobs[id]
			if !ok || j.State == "ready" || j.State == "failed" {
				p.mu.Unlock()
				continue
			}
			j.State = "running"
			p.jobs[id] = j
			p.mu.Unlock()
			task, cancel := context.WithTimeout(ctx, 30*time.Minute)
			out, e := p.run(task, j.Server, j.Input)
			cancel()
			j.State = "ready"
			if e != nil {
				j.State = "failed"
				j.Error = e.Error()
			} else {
				j.Output, e = json.Marshal(out)
				if e != nil || len(j.Output) > 512<<10 {
					j.State = "failed"
					j.Error = "job result exceeds bound"
					j.Output = nil
				}
			}
			p.mu.Lock()
			if _, ok = p.jobs[id]; ok {
				if e = storeio.Save(p.root, id+".json", j); e != nil {
					j.State = "failed"
					j.Error = "job result persistence failed"
				}
				p.jobs[id] = j
			}
			p.mu.Unlock()
		}
	}
}
func (p *Pool) Submit(server, request string, input any) (Job, error) {
	if server == "" || request == "" || len(request) > 128 {
		return Job{}, errors.New("bounded request identity required")
	}
	b, e := json.Marshal(input)
	if e != nil || len(b) > 64<<10 {
		return Job{}, errors.New("job input bound")
	}
	id := storeio.Hash([]byte(server + "\x00" + request))
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel == nil {
		return Job{}, errors.New("job worker unavailable")
	}
	if j, ok := p.jobs[id]; ok {
		if string(j.Input) != string(b) {
			return Job{}, errors.New("request identity conflict")
		}
		return j, nil
	}
	if len(p.jobs) >= 1024 || len(p.queue) == cap(p.queue) {
		return Job{}, errors.New("job queue full; release completed handles")
	}
	j := Job{ID: id, Server: server, State: "pending", Input: b}
	if e = storeio.Save(p.root, id+".json", j); e != nil {
		return Job{}, e
	}
	p.jobs[id] = j
	p.queue <- id
	return j, nil
}
func (p *Pool) Get(server, id string) (Job, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	j, ok := p.jobs[id]
	if !ok || j.Server != server {
		return Job{}, errors.New("unknown scoped handle")
	}
	return j, nil
}
func (p *Pool) Release(server, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	j, ok := p.jobs[id]
	if !ok {
		return nil
	}
	if j.Server != server {
		return errors.New("wrong handle owner")
	}
	if j.State != "ready" && j.State != "failed" {
		return errors.New("job still running")
	}
	if e := os.Remove(filepath.Join(p.root, id+".json")); e != nil {
		return e
	}
	delete(p.jobs, id)
	return nil
}
func (p *Pool) Close() {
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.Unlock()
	p.wg.Wait()
}

// ReleasePrefix retires only completed workshop jobs belonging to one server's
// durable local operation, never queued/running mutations.
func (p *Pool) ReleasePrefix(server, prefix string) error {
	if len(prefix) != 32 {
		return errors.New("invalid completed operation identity")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, j := range p.jobs {
		if j.Server != server || j.State != "ready" && j.State != "failed" {
			continue
		}
		var r struct {
			RequestID string `json:"request_id"`
		}
		if json.Unmarshal(j.Input, &r) != nil || !strings.HasPrefix(r.RequestID, prefix+"-") {
			continue
		}
		if e := os.Remove(filepath.Join(p.root, id+".json")); e != nil {
			return e
		}
		delete(p.jobs, id)
	}
	return nil
}
