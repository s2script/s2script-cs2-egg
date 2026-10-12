// SPDX-License-Identifier: GPL-3.0-or-later
package nodejobs

import (
	"context"
	"encoding/json"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRestartResumesDurablePendingAndRejectsIdentityReuse(t *testing.T) {
	root := t.TempDir()
	id := storeio.Hash([]byte("one\x00stable"))
	j := Job{ID: id, Server: "one", State: "pending", Input: json.RawMessage(`{"key":"value"}`)}
	if e := storeio.Save(root, id+".json", j); e != nil {
		t.Fatal(e)
	}
	p, e := New(root, func(context.Context, string, json.RawMessage) (any, error) { return "recovered", nil })
	if e != nil {
		t.Fatal(e)
	}
	if e = p.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	for range 100 {
		j, e = p.Get("one", id)
		if e != nil {
			t.Fatal(e)
		}
		if j.State == "ready" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if j.State != "ready" {
		t.Fatal(j)
	}
	if _, e = p.Submit("one", "stable", map[string]string{"key": "different"}); e == nil {
		t.Fatal("stable request identity overwritten")
	}
	var persisted Job
	if e = storeio.JSON(filepath.Clean(root), id+".json", &persisted); e != nil || persisted.State != "ready" {
		t.Fatal("recovery result not durable", e)
	}
}
func TestRestartIgnoresUnpublishedTemporaryRecord(t *testing.T) {
	root := t.TempDir()
	if e := os.WriteFile(filepath.Join(root, ".tmp-"+storeio.ID()), []byte("interrupted"), 0600); e != nil {
		t.Fatal(e)
	}
	p, e := New(root, func(context.Context, string, json.RawMessage) (any, error) { return nil, nil })
	if e != nil {
		t.Fatal(e)
	}
	if e = p.Start(context.Background()); e != nil {
		t.Fatal("unpublished temp prevents restart", e)
	}
	p.Close()
}
