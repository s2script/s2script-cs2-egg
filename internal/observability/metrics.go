// SPDX-License-Identifier: GPL-3.0-or-later
package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

func ValidateMetricsAddress(address string) error {
	host, p, e := net.SplitHostPort(address)
	if e != nil {
		return errors.New("metrics requires literal loopback IP and port")
	}
	ip := net.ParseIP(host)
	port, e := strconv.Atoi(p)
	if e != nil || port < 1 || port > 65535 || ip == nil || !ip.IsLoopback() {
		return errors.New("metrics requires literal loopback IP and port")
	}
	return nil
}
func WriteMetrics(w io.Writer, r Report) {
	ids := []string{}
	for id := range r.Servers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := r.Servers[id]
		state := "unknown"
		switch s.State {
		case "preparing", "starting", "ready", "unresponsive", "stopping", "stopped", "lost_contact":
			state = s.State
		}
		fmt.Fprintf(w, "s2node_server_state{server=%q,state=%q} 1\n", id, state)
		known := 0
		if s.Players.Known && s.Players.Count >= 0 && s.Ready && s.State == "ready" {
			known = 1
			fmt.Fprintf(w, "s2node_players{server=%q} %d\n", id, s.Players.Count)
		}
		fmt.Fprintf(w, "s2node_players_known{server=%q} %d\n", id, known)
	}
	for name, h := range r.Modules {
		state := "failed"
		switch h.Status {
		case "healthy", "degraded":
			state = h.Status
		}
		fmt.Fprintf(w, "s2node_module_health{module=%q,state=%q} 1\n", name, state)
	}
}

// ServeMetrics serves no control or file routes. Handlers reject excess concurrency.
func ServeMetrics(ctx context.Context, address string, snapshot func() Report) (func(context.Context) error, error) {
	if e := ValidateMetricsAddress(address); e != nil {
		return nil, e
	}
	l, e := net.Listen("tcp", address)
	if e != nil {
		return nil, e
	}
	slots := make(chan struct{}, 8)
	srv := &http.Server{ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8192, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" || r.Method != "GET" {
			http.NotFound(w, r)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "busy", 503)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		WriteMetrics(w, snapshot())
	})}
	go srv.Serve(&limitedListener{Listener: l, slots: make(chan struct{}, 32)})
	stop := context.AfterFunc(ctx, func() { srv.Close() })
	return func(c context.Context) error { stop(); return srv.Shutdown(c) }, nil
}

type limitedListener struct {
	net.Listener
	slots chan struct{}
}
type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error { e := c.Conn.Close(); c.once.Do(c.release); return e }
func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		c, e := l.Listener.Accept()
		if e != nil {
			return nil, e
		}
		select {
		case l.slots <- struct{}{}:
			return &limitedConn{Conn: c, release: func() { <-l.slots }}, nil
		default:
			c.Close()
		}
	}
}
