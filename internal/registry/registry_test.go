package registry

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The root catalog carries every namespace: the official `apps` shelf, the
// `ply` layers nothing can one-click install, and whatever users publish.
const rootState = `{"packages":[
 {"namespace":"ply","owner":"ply","name":"node","type":"layer","versions":[{"version":"24.18.1"}]},
 {"namespace":"apps","owner":"apps","name":"postgres","type":"app","versions":[{"version":"17.10.6"}]},
 {"namespace":"iluxav","owner":"iluxav","name":"demo-web-app","type":"app","versions":[{"version":"0.1.1"},{"version":"0.1.0"}]}
]}`

func serve(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A layer is not a runnable app — it must never reach the deploy form.
func TestLayersAreExcluded(t *testing.T) {
	apps, err := fetch(serve(t, rootState))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range apps {
		if a.Name == "node" {
			t.Fatalf("layer `node` leaked into the catalog: %+v", apps)
		}
	}
	if len(apps) != 2 {
		t.Fatalf("want 2 runnable apps, got %d: %+v", len(apps), apps)
	}
}

// The deployment spec's `app =` must name the namespace for anything off the
// official shelf — a bare name resolves against apps/ and 404s at reconcile.
func TestRefIsNamespacedOffTheOfficialShelf(t *testing.T) {
	apps, err := fetch(serve(t, rootState))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range apps {
		got[a.Name] = a.Ref
	}
	if got["postgres"] != "postgres" {
		t.Errorf("official app should stay bare, got %q", got["postgres"])
	}
	if got["demo-web-app"] != "iluxav/demo-web-app" {
		t.Errorf("user app must carry its namespace, got %q", got["demo-web-app"])
	}
}

// Official apps lead the list; a stranger's package must not outrank postgres
// just because its name sorts earlier.
func TestOfficialAppsSortFirst(t *testing.T) {
	apps, err := fetch(serve(t, rootState))
	if err != nil {
		t.Fatal(err)
	}
	if apps[0].Namespace != "apps" {
		t.Fatalf("want an official app first, got %+v", apps[0])
	}
}
