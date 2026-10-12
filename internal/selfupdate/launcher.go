// SPDX-License-Identifier: GPL-3.0-or-later
package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/buildinfo"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Readiness struct {
	Transaction string `json:"transaction"`
	Version     string `json:"version"`
	Revision    string `json:"revision"`
}
type Child struct {
	Ready <-chan Readiness
	Exit  <-chan int
	Stop  func()
}
type StartFunc func(context.Context, string, string, string) (Child, error)
type Launcher struct {
	Manager    *Manager
	ConfigPath string
	Timeout    time.Duration
	Start      StartFunc
	Notify     func() error
}

// Run never invokes a service manager. A dedicated exit requests a manager restart;
// explicit cancellation stops the child and leaves any unfinished trial recoverable.
func (l Launcher) Run(ctx context.Context) int {
	m := l.Manager
	if m == nil || m.root() != nil || l.Timeout < 5*time.Second || l.Timeout > 300*time.Second {
		return FailureExit
	}
	dir, err := artifact.OpenDirectory(m.Root)
	if err != nil {
		return FailureExit
	}
	lock, err := artifact.OpenRegular(dir, ".launcher-lock", syscall.O_RDWR|syscall.O_CREAT, 0600)
	dir.Close()
	if err != nil {
		return FailureExit
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return FailureExit
	}
	start := l.Start
	if start == nil {
		start = startChild
	}
	j, err := m.Status()
	if err != nil || j.Phase == "failed" || j.Phase == "rollback-started" {
		return FailureExit
	}
	fallback := j.Phase == "trial-started"
	trial := j.Phase == "pending"
	if fallback {
		j.FailedVersion = j.Candidate
		j.Phase = "rollback-started"
		j.Outcome = "interrupted_trial"
		j.At = time.Now().UTC()
		if m.save(j) != nil {
			return FailureExit
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		if ctx.Err() != nil {
			return 0
		}
		version, digest := j.Current, j.CurrentSHA256
		if trial {
			version, digest = j.Candidate, j.CandidateSHA256
			j.Phase = "trial-started"
			j.At = time.Now().UTC()
			if m.save(j) != nil {
				return FailureExit
			}
		}
		r, err := m.release(version)
		if err == nil && r.SHA256 != digest {
			err = errors.New("selected executable digest changed")
		}
		if err == nil && trial && m.Preflight != nil {
			err = m.Preflight(ctx, r.Manifest)
		}
		var child Child
		if err == nil {
			child, err = start(ctx, m.binary(version), l.ConfigPath, j.Transaction)
		}
		accepted := false
		if err == nil {
			if child.Stop == nil || child.Ready == nil || child.Exit == nil {
				return FailureExit
			}
			timer := time.NewTimer(l.Timeout)
			select {
			case <-ctx.Done():
				timer.Stop()
				child.Stop()
				return 0
			case <-timer.C:
				err = errors.New("readiness timeout")
			case _, ok := <-child.Exit:
				if !ok {
					err = errors.New("child exit unavailable")
				} else {
					err = errors.New("child exited before readiness")
				}
			case ready, ok := <-child.Ready:
				if ok && ready.Transaction == j.Transaction && ready.Version == version && ready.Revision == r.SourceRevision {
					accepted = true
				} else {
					err = errors.New("invalid private child readiness")
				}
			}
			timer.Stop()
			if accepted {
				select {
				case <-child.Exit:
					accepted = false
					err = errors.New("child exited during readiness")
				default:
				}
			}
			if ctx.Err() != nil {
				child.Stop()
				return 0
			}
			if !accepted {
				child.Stop()
			}
		}
		if !accepted {
			if ctx.Err() != nil {
				return 0
			}
			if trial {
				j.FailedVersion = j.Candidate
				j.Phase = "rollback-started"
				j.Outcome = "trial_failed"
				j.At = time.Now().UTC()
				if m.save(j) != nil {
					return FailureExit
				}
				trial = false
				fallback = true
				continue
			}
			j.Phase = "failed"
			j.Outcome = "startup_failed"
			j.At = time.Now().UTC()
			_ = m.save(j)
			return FailureExit
		}
		if trial || fallback {
			if trial {
				j.Previous = j.Current
				j.Current = version
				j.CurrentSHA256 = digest
				j.Outcome = "activated"
				j.FailedVersion = ""
			} else {
				j.Outcome = "rolled_back"
			}
			j.Candidate = ""
			j.CandidateSHA256 = ""
			j.Phase = "good"
			j.At = time.Now().UTC()
			if m.save(j) != nil {
				child.Stop()
				return FailureExit
			}
		}
		notify := l.Notify
		if notify == nil {
			notify = notifyReady
		}
		if notify() != nil {
			child.Stop()
			return FailureExit
		}
		select {
		case <-ctx.Done():
			child.Stop()
			return 0
		case code, ok := <-child.Exit:
			child.Stop()
			if ctx.Err() != nil {
				return 0
			}
			if !ok {
				return FailureExit
			}
			if code == ActivationExit {
				current, e := m.Status()
				if e != nil || current.Phase != "pending" {
					return FailureExit
				}
			}
			return code
		}
	}
	return FailureExit
}
func startChild(ctx context.Context, path, config, tx string) (Child, error) {
	read, write, err := os.Pipe()
	if err != nil {
		return Child{}, err
	}
	cmd := exec.Command(path, "run", "--config", config)
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = []*os.File{write}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "S2NODE_READY_") && !strings.HasPrefix(v, "S2NODE_TRIAL_") && !strings.HasPrefix(v, "NOTIFY_SOCKET=") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "S2NODE_READY_FD=3", "S2NODE_TRIAL_ID="+tx)
	if err = cmd.Start(); err != nil {
		read.Close()
		write.Close()
		return Child{}, err
	}
	write.Close()
	ready := make(chan Readiness, 1)
	exit := make(chan int, 1)
	done := make(chan struct{})
	go func() {
		defer read.Close()
		defer close(ready)
		var r Readiness
		if json.NewDecoder(io.LimitReader(read, 4096)).Decode(&r) == nil {
			ready <- r
		}
	}()
	go func() {
		err := cmd.Wait()
		code := 0
		if err != nil {
			code = 1
			var e *exec.ExitError
			if errors.As(err, &e) && e.ExitCode() >= 0 {
				code = e.ExitCode()
			}
		}
		exit <- code
		close(done)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			read.Close()
			select {
			case <-done:
				return
			default:
			}
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(35 * time.Second):
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				<-done
			}
		})
	}
	return Child{Ready: ready, Exit: exit, Stop: stop}, nil
}

// ReportReady is invoked only after Core.Ready and required initialization checks.
func ReportReady() error {
	fd := os.Getenv("S2NODE_READY_FD")
	tx := os.Getenv("S2NODE_TRIAL_ID")
	if fd == "" && tx == "" {
		return nil
	}
	if fd != "3" || !storeio.Digest(tx) {
		return errors.New("invalid launcher readiness channel")
	}
	if err := syscall.SetNonblock(3, true); err != nil {
		return err
	}
	f := os.NewFile(3, "node-readiness")
	if f == nil {
		return errors.New("missing readiness descriptor")
	}
	defer f.Close()
	if err := f.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	return json.NewEncoder(f).Encode(Readiness{Transaction: tx, Version: buildinfo.Version, Revision: buildinfo.Revision})
}
func notifyReady() error {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return nil
	}
	if socket[0] != '/' && socket[0] != '@' {
		return errors.New("invalid service notification socket")
	}
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetWriteDeadline(time.Now().Add(time.Second))
	_, err = c.Write([]byte("READY=1"))
	return err
}
