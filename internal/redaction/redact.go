// SPDX-License-Identifier: GPL-3.0-or-later
// Package redaction is the single known-secret redaction boundary. It does not
// claim to recognize arbitrary unknown credentials or sanitize binary dumps.
package redaction

import (
	"bytes"
	"errors"
	"io"
	"net/url"
	"path"
	"strings"
)

const Marker = "[REDACTED]"

type Stream struct {
	out     io.Writer
	secrets [][]byte
	pending []byte
	longest int
	closed  bool
	err     error
}

func NewStream(out io.Writer, secrets []string) (*Stream, error) {
	if out == nil || len(secrets) > 128 {
		return nil, errors.New("invalid redaction configuration")
	}
	s := &Stream{out: out, longest: 1}
	total := 0
	for _, v := range secrets {
		if v == "" {
			continue
		}
		total += len(v)
		if len(v) > 16<<10 || total > 64<<10 {
			return nil, errors.New("redaction secret bound exceeded")
		}
		s.secrets = append(s.secrets, []byte(v))
		if len(v) > s.longest {
			s.longest = len(v)
		}
	}
	return s, nil
}
func (s *Stream) drain(final bool) {
	for len(s.pending) > 0 && (final || len(s.pending) >= s.longest) {
		n := 0
		for _, secret := range s.secrets {
			if len(secret) > n && bytes.HasPrefix(s.pending, secret) {
				n = len(secret)
			}
		}
		var b []byte
		if n > 0 {
			b = []byte(Marker)
		} else {
			n = 1
			b = s.pending[:1]
		}
		if s.err == nil {
			var written int
			written, s.err = s.out.Write(b)
			if s.err == nil && written != len(b) {
				s.err = io.ErrShortWrite
			}
		}
		s.pending = s.pending[n:]
	}
}
func (s *Stream) Write(b []byte) (int, error) {
	if s.closed {
		return 0, errors.New("redactor closed")
	}
	for _, v := range b {
		s.pending = append(s.pending, v)
		s.drain(false)
	}
	return len(b), s.err
}
func (s *Stream) Close() error {
	if !s.closed {
		s.closed = true
		s.drain(true)
	}
	return s.err
}
func Redact(text string, secrets []string) string {
	var b strings.Builder
	s, e := NewStream(&b, secrets)
	if e != nil {
		return "[redaction configuration invalid; content withheld]"
	}
	_, _ = s.Write([]byte(text))
	_ = s.Close()
	return b.String()
}
func Secrets(values ...string) []string {
	out := append([]string(nil), values...)
	for _, v := range values {
		if u, e := url.Parse(v); e == nil && u.Scheme != "" && u.Path != "" {
			out = append(out, path.Base(u.Path))
		}
	}
	return out
}
