package plystate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStackMembersAndTagging(t *testing.T) {
	root := t.TempDir()
	status := filepath.Join(root, ".status")
	if err := os.MkdirAll(status, 0o755); err != nil {
		t.Fatal(err)
	}
	// one stack `qa` owning three apps; a stray non-.members file is ignored
	if err := os.WriteFile(filepath.Join(status, "qa.members"), []byte("db\nserver\nweb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(status, "qa.status"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := Paths{Deployments: root}

	members := StackMembers(p)
	if members["server"] != "qa" || members["db"] != "qa" || members["web"] != "qa" {
		t.Fatalf("members map wrong: %v", members)
	}
	if _, ok := members["dashboard"]; ok {
		t.Fatal("dashboard is not a member")
	}

	insts := []Instance{
		{App: "web"}, {App: "dashboard"}, {App: "db"}, {App: "server"},
	}
	apps := AppsWithStacks(insts, members)
	// standalone first, then the stack's members contiguous & alphabetical
	var order []string
	for _, a := range apps {
		order = append(order, a.Name+"/"+a.Stack)
	}
	want := []string{"dashboard/", "db/qa", "server/qa", "web/qa"}
	if len(order) != len(want) {
		t.Fatalf("got %v want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order[%d]=%q want %q (full %v)", i, order[i], want[i], order)
		}
	}
}

// No .status dir (older ply / no stacks) → empty map, apps untouched.
func TestStackMembersAbsentIsGraceful(t *testing.T) {
	p := Paths{Deployments: t.TempDir()}
	if m := StackMembers(p); len(m) != 0 {
		t.Fatalf("want empty, got %v", m)
	}
	apps := AppsWithStacks([]Instance{{App: "solo"}}, nil)
	if len(apps) != 1 || apps[0].Stack != "" {
		t.Fatalf("standalone app should be untouched: %+v", apps)
	}
}
