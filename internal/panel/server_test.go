package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/geekermd/nodepanel/internal/shared"
	"github.com/geekermd/nodepanel/internal/store"
)

func newTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateSettings(func(s *store.Settings) { s.PasswordHash = shared.HashToken("pw") }); err != nil {
		t.Fatal(err)
	}
	return NewServer(st, NewPoller(st), nil), st
}

func TestLoginRequiredForAPI(t *testing.T) {
	srv, _ := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, path := range []string{"/api/overview", "/api/nodes", "/api/todos", "/api/settings"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without session = %d, want 401", path, resp.StatusCode)
		}
	}
}

func TestLoginAndNodeCRUD(t *testing.T) {
	srv, st := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Wrong password must be rejected.
	bad, _ := http.Post(ts.URL+"/api/login", "application/json", strings.NewReader(`{"password":"nope"}`))
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d, want 401", bad.StatusCode)
	}
	bad.Body.Close()

	// Correct password issues a session cookie.
	resp, err := http.Post(ts.URL+"/api/login", "application/json", strings.NewReader(`{"password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "np_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}
	do := func(method, path, body string) *http.Response {
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	// Create a node.
	r := do("POST", "/api/nodes", `{"name":"web01","host":"10.0.0.1","port":8899,"token":"tok","note":"主站","group":"生产"}`)
	if r.StatusCode != 200 {
		t.Fatalf("create node = %d", r.StatusCode)
	}
	var created struct {
		Node store.Node `json:"node"`
		URL  string     `json:"url"`
	}
	_ = json.NewDecoder(r.Body).Decode(&created)
	r.Body.Close()
	if created.Node.ID == "" {
		t.Fatal("node id missing")
	}
	if created.URL != "http://10.0.0.1:8899" {
		t.Fatalf("agent url = %q", created.URL)
	}

	// Validation: a token is mandatory.
	r = do("POST", "/api/nodes", `{"name":"x","host":"1.2.3.4","port":1}`)
	if r.StatusCode != 400 {
		t.Fatalf("node without token should be rejected, got %d", r.StatusCode)
	}
	r.Body.Close()

	// Update the note.
	r = do("PATCH", "/api/nodes/"+created.Node.ID, `{"note":"改了备注"}`)
	if r.StatusCode != 200 {
		t.Fatalf("patch = %d", r.StatusCode)
	}
	r.Body.Close()
	if got := st.Node(created.Node.ID); got == nil || got.Note != "改了备注" {
		t.Fatalf("note not applied: %+v", got)
	}

	// Metrics endpoint answers even with no history.
	r = do("GET", "/api/nodes/"+created.Node.ID+"/metrics?span=3600", "")
	if r.StatusCode != 200 {
		t.Fatalf("metrics = %d", r.StatusCode)
	}
	var m struct {
		Tier    string `json:"tier"`
		Samples []any  `json:"samples"`
	}
	_ = json.NewDecoder(r.Body).Decode(&m)
	r.Body.Close()
	if m.Tier == "" {
		t.Fatalf("metrics tier missing: %+v", m)
	}

	// TODOs.
	r = do("POST", "/api/todos", `{"text":"续费","node_id":"`+created.Node.ID+`"}`)
	if r.StatusCode != 200 {
		t.Fatalf("todo create = %d", r.StatusCode)
	}
	var td struct {
		Todo store.Todo `json:"todo"`
	}
	_ = json.NewDecoder(r.Body).Decode(&td)
	r.Body.Close()
	r = do("PATCH", "/api/todos/"+td.Todo.ID, `{"done":true}`)
	if r.StatusCode != 200 {
		t.Fatalf("todo patch = %d", r.StatusCode)
	}
	r.Body.Close()

	// Overview reflects the node.
	r = do("GET", "/api/overview", "")
	var ov struct {
		Summary struct {
			Nodes     int `json:"nodes"`
			TodosOpen int `json:"todos_open"`
		} `json:"summary"`
		Nodes []struct {
			Node store.Node `json:"node"`
		} `json:"nodes"`
	}
	_ = json.NewDecoder(r.Body).Decode(&ov)
	r.Body.Close()
	if ov.Summary.Nodes != 1 || len(ov.Nodes) != 1 {
		t.Fatalf("overview summary wrong: %+v", ov.Summary)
	}
	if ov.Summary.TodosOpen != 0 {
		t.Fatalf("completed todo counted as open: %+v", ov.Summary)
	}

	// Delete.
	r = do("DELETE", "/api/nodes/"+created.Node.ID, "")
	if r.StatusCode != 200 {
		t.Fatalf("delete = %d", r.StatusCode)
	}
	r.Body.Close()
	if len(st.ListNodes()) != 0 {
		t.Fatal("node still present after delete")
	}
}

func TestAgentURLModes(t *testing.T) {
	cases := []struct {
		node store.Node
		want string
	}{
		{store.Node{Host: "1.2.3.4", Port: 8899, Mode: store.ModeDirect, Scheme: "http"}, "http://1.2.3.4:8899"},
		{store.Node{Host: "srv.example.com", Port: 8899, Mode: store.ModeDirect, Scheme: "http"}, "http://srv.example.com:8899"},
		{store.Node{Host: "panel.example.com", Port: 0, Mode: store.ModeDomain, Scheme: "https"}, "https://panel.example.com"},
		{store.Node{Host: "abc-def.trycloudflare.com", Port: 0, Mode: store.ModeTunnel, Scheme: "http"}, "https://abc-def.trycloudflare.com"},
		{store.Node{Host: "https://node1.example.com", Port: 0, Mode: store.ModeTunnel, Scheme: "https"}, "https://node1.example.com"},
		{store.Node{Host: "1.2.3.4", Port: 443, Mode: store.ModeDirect, Scheme: "https"}, "https://1.2.3.4"},
	}
	for _, c := range cases {
		if got := AgentURL(&c.node); got != c.want {
			t.Errorf("AgentURL(%+v) = %q, want %q", c.node, got, c.want)
		}
	}
}

func TestAllowedPath(t *testing.T) {
	allow := []string{"/var/log", "/var/log/nginx/access.log", "/etc/nginx", "/tmp", "/home/md/app", "/opt/x", "/srv"}
	deny := []string{"/", "/root", "/root/.ssh/id_rsa", "/var/log/../../etc/shadow", "relative", "", "/proc/self/environ", "/var/logx"}
	for _, p := range allow {
		if !allowedPath(p) {
			t.Errorf("allowedPath(%q) = false, want true", p)
		}
	}
	for _, p := range deny {
		if allowedPath(p) {
			t.Errorf("allowedPath(%q) = true, want false", p)
		}
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/var/log"); got != "'/var/log'" {
		t.Fatalf("shellQuote = %s", got)
	}
	if got := shellQuote("a'b"); got != `'a'\''b'` {
		t.Fatalf("shellQuote escape = %s", got)
	}
}
