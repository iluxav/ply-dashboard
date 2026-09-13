package plystate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeInventory(t *testing.T, p Paths, body string) {
	t.Helper()
	dir := filepath.Join(p.Deployments, ".status")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "volumes.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVolumesParse(t *testing.T) {
	p := Paths{Deployments: t.TempDir()}
	writeInventory(t, p, `[
	  {"app":"postgres","volume":"data.1","status":"in use","bytes":null},
	  {"app":"db","volume":"data.1","status":"orphaned","bytes":40632320},
	  {"app":"cache","volume":"rdb.1","status":"idle","bytes":1536}
	]`)

	all := Volumes(p)
	if len(all) != 3 {
		t.Fatalf("want 3 volumes, got %d", len(all))
	}
	// null bytes → HasBytes false, Size "?"
	if all[0].HasBytes || all[0].Size() != "?" {
		t.Fatalf("in-use volume should have no size: %+v", all[0])
	}
	if !all[1].HasBytes || all[1].Size() != "38.8 MiB" {
		t.Fatalf("orphan size wrong: %q", all[1].Size())
	}

	orphans := OrphanVolumes(p)
	if len(orphans) != 1 || orphans[0].App != "db" {
		t.Fatalf("want one orphan (db), got %+v", orphans)
	}
	if orphans[0].ShowName() { // "data.1" adds nothing
		t.Fatal("ShowName should be false for a data.<slot> volume")
	}
	if !all[2].ShowName() { // "rdb.1" is worth showing
		t.Fatal("ShowName should be true for a non-data volume")
	}
}

func TestVolumesMissingIsNilNotError(t *testing.T) {
	if v := Volumes(Paths{Deployments: t.TempDir()}); v != nil {
		t.Fatalf("missing inventory should be nil, got %v", v)
	}
}

func TestRequestReapAppendsAndDedupes(t *testing.T) {
	p := Paths{Deployments: t.TempDir()}
	if err := RequestReap(p, "db", "postgres"); err != nil {
		t.Fatal(err)
	}
	// re-request one existing + one new: only the new is appended
	if err := RequestReap(p, "db", "cache"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(p.Deployments, ".status", "reap"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		got = append(got, l)
	}
	want := []string{"db", "postgres", "cache"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("reap queue = %v, want %v", got, want)
	}
	// blanks are skipped, empty request is a no-op
	if err := RequestReap(p, "", "  "); err != nil {
		t.Fatal(err)
	}
	raw2, _ := os.ReadFile(filepath.Join(p.Deployments, ".status", "reap"))
	if string(raw2) != string(raw) {
		t.Fatal("blank/empty reap request should not change the file")
	}
}
