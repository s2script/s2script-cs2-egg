// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
)

func inputReader(in io.Reader) (io.Reader, func(), error) {
	switch r := in.(type) {
	case nil:
		return nil, func() {}, nil
	case *os.File:
		// Opening /proc/self/fd creates an independent open-file description, unlike
		// dup: setting O_NONBLOCK must never change the caller's descriptor flags.
		f, e := os.OpenFile("/proc/self/fd/"+itoa(int(r.Fd())), os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if e != nil {
			return nil, nil, errors.New("cannot open cancellable console input")
		}
		return f, func() { f.Close() }, nil
	case *strings.Reader, *bytes.Reader, *bytes.Buffer:
		return r, func() {}, nil
	case io.ReadCloser:
		var once sync.Once
		return r, func() { once.Do(func() { r.Close() }) }, nil
	default:
		return nil, nil, errors.New("console input requires a file, finite standard reader, or owned io.ReadCloser")
	}
}
func forwardInput(in io.Reader, out *os.File, stopping chan<- string, done <-chan struct{}, finished chan<- struct{}, guards ...func(string) (bool, string)) {
	defer close(finished)
	if in == nil {
		return
	}
	s := bufio.NewScanner(in)
	s.Buffer(make([]byte, 4096), 64<<10)
	for s.Scan() {
		line := s.Text()
		if len(guards) > 0 {
			handled, why := guards[0](line)
			if handled {
				if why != "" {
					select {
					case stopping <- why:
					case <-done:
						return
					}
				}
				continue
			}
		}
		if strings.EqualFold(strings.TrimSpace(line), "quit") || strings.EqualFold(strings.TrimSpace(line), "exit") {
			select {
			case stopping <- "console_stop":
			case <-done:
				return
			}
		}
		if _, e := io.WriteString(out, line+"\n"); e != nil {
			return
		}
	}
}

// consoleWriter buffers whole lines so credentials split across Write calls
// are redacted together. Oversized lines are discarded, never emitted in parts.
type consoleWriter struct {
	mu       *sync.Mutex
	out      io.Writer
	emit     emitFunc
	redact   func(string) string
	exclude  []string
	stream   string
	pending  []byte
	overflow bool
}

func (w *consoleWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	for _, b := range p {
		if b == '\n' {
			w.flushLocked()
			continue
		}
		if w.overflow {
			continue
		}
		if len(w.pending) >= 64<<10 {
			w.pending = nil
			w.overflow = true
			continue
		}
		w.pending = append(w.pending, b)
	}
	return n, nil
}
func (w *consoleWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) > 0 || w.overflow {
		w.flushLocked()
	}
}
func (w *consoleWriter) flushLocked() {
	line := string(w.pending)
	if w.overflow {
		line = "[console line exceeded limit]"
	}
	line = w.redact(line)
	w.pending = nil
	w.overflow = false
	w.emit("server.console", "", map[string]string{"stream": w.stream, "line": line})
	for _, pattern := range w.exclude {
		if strings.Contains(line, pattern) {
			return
		}
	}
	if w.out != nil {
		io.WriteString(w.out, strings.ReplaceAll(line, "S2EGG_READY", "[reserved readiness marker]")+"\n")
	}
}
