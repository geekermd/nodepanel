package shared

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTokenHashing(t *testing.T) {
	h := HashToken("s3cret")
	if h == "" || h == "s3cret" {
		t.Fatalf("hash looks wrong: %q", h)
	}
	if !CheckToken(h, "s3cret") {
		t.Fatal("valid token rejected")
	}
	if CheckToken(h, "s3cret ") || CheckToken(h, "") || CheckToken("", "s3cret") {
		t.Fatal("invalid token accepted")
	}
	if HashToken("a") == HashToken("b") {
		t.Fatal("hash collision")
	}
	// 两次生成的随机令牌不应重复，长度也要够。
	a, b := RandomToken(16), RandomToken(16)
	if a == b || len(a) < 16 {
		t.Fatalf("RandomToken weak: %q %q", a, b)
	}
}

func TestBaseURL(t *testing.T) {
	cases := []struct {
		host   string
		port   int
		scheme string
		want   string
	}{
		{"1.2.3.4", 8899, "http", "http://1.2.3.4:8899"},
		{"1.2.3.4", 8899, "https", "https://1.2.3.4:8899"},
		{"http://1.2.3.4:8899/", 8899, "http", "http://1.2.3.4:8899"},
		{"srv.example.com/", 8899, "http", "http://srv.example.com:8899"},
		{"panel.example.com", 0, "https", "https://panel.example.com"},
		{"panel.example.com", 80, "http", "http://panel.example.com"},
		{"panel.example.com", 443, "https", "https://panel.example.com"},
		{"abc.trycloudflare.com", 0, "http", "https://abc.trycloudflare.com"},
		{"abc.trycloudflare.com", 443, "http", "https://abc.trycloudflare.com"},
		{"node.cfargotunnel.com", 0, "http", "https://node.cfargotunnel.com"},
		{"1.2.3.4:9000", 8899, "http", "http://1.2.3.4:9000"},
		{"", 8899, "http", ""},
	}
	for _, c := range cases {
		if got := BaseURL(c.host, c.port, c.scheme); got != c.want {
			t.Errorf("BaseURL(%q,%d,%q) = %q, want %q", c.host, c.port, c.scheme, got, c.want)
		}
	}
}

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"  https://a.example.com/path?x=1 ": "a.example.com",
		"http://1.2.3.4:8899":               "1.2.3.4:8899",
		"a.example.com/":                    "a.example.com",
		"a.example.com":                     "a.example.com",
	}
	for in, want := range cases {
		if got := NormalizeHost(in); got != want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBearerTokenSources(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/stats?token=fromquery", nil)
	if got := BearerToken(r); got != "fromquery" {
		t.Fatalf("query token = %q", got)
	}
	r = httptest.NewRequest("GET", "/api/v1/stats", nil)
	r.Header.Set("Authorization", "Bearer fromheader")
	if got := BearerToken(r); got != "fromheader" {
		t.Fatalf("header token = %q", got)
	}
	r = httptest.NewRequest("GET", "/api/v1/stats", nil)
	r.AddCookie(&http.Cookie{Name: "np_token", Value: "fromcookie"})
	if got := BearerToken(r); got != "fromcookie" {
		t.Fatalf("cookie token = %q", got)
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.9:12345"
	if got := ClientIP(r); got != "10.0.0.9" {
		t.Fatalf("plain ip = %q", got)
	}
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	if got := ClientIP(r); got != "203.0.113.7" {
		t.Fatalf("xff = %q", got)
	}
	r.Header.Set("CF-Connecting-IP", "198.51.100.5")
	if got := ClientIP(r); got != "198.51.100.5" {
		t.Fatalf("cf ip = %q", got)
	}
}

func TestFormatHelpers(t *testing.T) {
	if got := Bytes(0); got != "0B" {
		t.Errorf("Bytes(0) = %q", got)
	}
	if got := Bytes(1536); got != "1.5KB" {
		t.Errorf("Bytes(1536) = %q", got)
	}
	if got := Bytes(1024 * 1024 * 3); got != "3.0MB" {
		t.Errorf("Bytes(3MiB) = %q", got)
	}
	if got := Rate(2048); got != "2.0KB/s" {
		t.Errorf("Rate(2048) = %q", got)
	}
	if got := Duration(0); got != "-" {
		t.Errorf("Duration(0) = %q", got)
	}
	if got := Duration(59); got != "59s" {
		t.Errorf("Duration(59) = %q", got)
	}
	if got := Duration(3661); got != "1h 1m" {
		t.Errorf("Duration(3661) = %q", got)
	}
	if got := Duration(90000); got != "1d 1h 0m" {
		t.Errorf("Duration(90000) = %q", got)
	}
	if got := Percent(12.34); got != "12.3%" {
		t.Errorf("Percent = %q", got)
	}
	if got := Round1(1.25); got != 1.3 {
		t.Errorf("Round1(1.25) = %v", got)
	}
	if got := Round1(1.24); got != 1.2 {
		t.Errorf("Round1(1.24) = %v", got)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "file.json")
	if err := WriteFileAtomic(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "hello" {
		t.Fatalf("read back = %q err=%v", b, err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v err=%v", st.Mode(), err)
	}
	// 覆盖写不能留下临时文件
	if err := WriteFileAtomic(path, []byte("world"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
	b, _ = os.ReadFile(path)
	if string(b) != "world" {
		t.Fatalf("overwrite = %q", b)
	}
}

func TestNowMS(t *testing.T) {
	ms := NowMS()
	if d := time.Since(time.UnixMilli(ms)); d > 5*time.Second || d < -5*time.Second {
		t.Fatalf("NowMS off by %v", d)
	}
}
