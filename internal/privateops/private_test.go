// SPDX-License-Identifier: GPL-3.0-or-later
package privateops

import (
	"context"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateOperationsRefuseLiveLeaseAndOversizeDump(t *testing.T) {
	v := t.TempDir()
	root := filepath.Join(t.TempDir(), "ops")
	os.MkdirAll(filepath.Join(v, "dumps"), 0700)
	os.WriteFile(filepath.Join(v, "dumps", "one.dmp"), make([]byte, 2048), 0600)
	p := Policy{Enabled: true, Root: root, DumpSource: "dumps", Dumps: true, DumpBytes: 1024, DumpTotalBytes: 4096, DumpFiles: 4}
	if e := p.Validate(); e != nil {
		t.Fatal(e)
	}
	u, e := runtimelease.Acquire(v)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Execute(context.Background(), v, p, Request{Operation: "dump-retain", RequestID: "fixture", ID: "one.dmp"}); e == nil {
		t.Fatal("live dump read accepted")
	}
	u()
	r, e := Execute(context.Background(), v, p, Request{Operation: "dump-retain", RequestID: "fixture", ID: "one.dmp"})
	if e != nil {
		t.Fatal(e)
	}
	if r.Skipped != "oversized dump; not truncated" {
		t.Fatal(r)
	}
}

func TestDumpRetryAtQuotaAndUnpublishedFilesCount(t *testing.T) {
	v := t.TempDir()
	root := filepath.Join(t.TempDir(), "ops")
	os.MkdirAll(filepath.Join(v, "dumps"), 0700)
	os.WriteFile(filepath.Join(v, "dumps", "one.dmp"), []byte("sensitive binary"), 0600)
	p := Policy{Enabled: true, Root: root, DumpSource: "dumps", Dumps: true, DumpBytes: 1024, DumpTotalBytes: 1024, DumpFiles: 1}
	first, e := Execute(context.Background(), v, p, Request{Operation: "dump-retain", RequestID: "fixture", ID: "one.dmp"})
	if e != nil {
		t.Fatal(e)
	}
	again, e := Execute(context.Background(), v, p, Request{Operation: "dump-retain", RequestID: "fixture", ID: "one.dmp"})
	if e != nil || again.ID != first.ID {
		t.Fatalf("retry changed artifact: %+v %v", again, e)
	}
	p.Root = filepath.Join(t.TempDir(), "orphan")
	os.MkdirAll(filepath.Join(p.Root, "dumps"), 0700)
	os.WriteFile(filepath.Join(p.Root, "dumps", ".dump-unpublished"), make([]byte, 1024), 0600)
	if _, e = Execute(context.Background(), v, p, Request{Operation: "dump-retain", RequestID: "fixture", ID: "one.dmp"}); e == nil {
		t.Fatal("unpublished dump bypassed disk quota")
	}
}

func TestDumpRequestSurvivesSourceReplacement(t *testing.T) {
	v := t.TempDir()
	root := filepath.Join(t.TempDir(), "ops")
	os.MkdirAll(filepath.Join(v, "dumps"), 0700)
	source := filepath.Join(v, "dumps", "one.dmp")
	os.WriteFile(source, []byte("original"), 0600)
	p := Policy{Enabled: true, Root: root, DumpSource: "dumps", Dumps: true}
	r := Request{Operation: "dump-retain", ID: "one.dmp", RequestID: "stable"}
	first, e := Execute(context.Background(), v, p, r)
	if e != nil {
		t.Fatal(e)
	}
	os.Remove(source)
	os.WriteFile(source, []byte("replacement"), 0600)
	again, e := Execute(context.Background(), v, p, r)
	if e != nil || again.ID != first.ID || again.Digest != first.Digest {
		t.Fatalf("request rebound to changed source: %+v %v", again, e)
	}
}

func TestDumpJournalRecoversOriginalSealedResultAfterHelperInterruption(t *testing.T) {
	v := t.TempDir()
	root := filepath.Join(t.TempDir(), "ops")
	os.MkdirAll(filepath.Join(v, "dumps"), 0700)
	source := filepath.Join(v, "dumps", "one.dmp")
	os.WriteFile(source, []byte("original"), 0600)
	p := Policy{Enabled: true, Root: root, DumpSource: "dumps", Dumps: true}
	r := Request{Operation: "dump-retain", ID: "one.dmp", RequestID: "interrupted"}
	first, e := Execute(context.Background(), v, p, r)
	if e != nil {
		t.Fatal(e)
	}
	journal := filepath.Join(root, "requests")
	name := dumpRecordName(v, r.RequestID)
	var rec dumpRecord
	if e = storeio.JSON(journal, name, &rec); e != nil {
		t.Fatal(e)
	}
	rec.Terminal = false
	rec.Result = Reply{}
	if e = storeio.Save(journal, name, rec); e != nil {
		t.Fatal(e)
	}
	os.Remove(source)
	os.WriteFile(source, []byte("replacement"), 0600)
	r.Operation = "dump-result"
	again, e := Execute(context.Background(), v, p, r)
	if e != nil || again.ID != first.ID || again.Digest != first.Digest {
		t.Fatal("original result not recovered", again, e)
	}
}
