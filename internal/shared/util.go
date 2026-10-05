// Package shared contains helpers used by both the panel and the node agent.
package shared

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Version is the release version of both binaries.
const Version = "1.0.0"

// RandomToken returns a URL-safe random token of n bytes of entropy.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Fall back to a time based token; should never happen.
		return hex.EncodeToString([]byte(strconv.FormatInt(time.Now().UnixNano(), 36)))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashToken returns the stored form of a password/token.
func HashToken(s string) string {
	sum := sha256.Sum256([]byte("nodepanel|" + s))
	return hex.EncodeToString(sum[:])
}

// CheckToken compares a plaintext token against a stored hash in constant time.
func CheckToken(hash, plain string) bool {
	if hash == "" || plain == "" {
		return false
	}
	got := HashToken(plain)
	return subtle.ConstantTimeCompare([]byte(hash), []byte(got)) == 1
}

// JSON writes v as a JSON response.
func JSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil {
		// Response already started; nothing sensible left to do.
		return
	}
}

// Error writes a JSON error object.
func Error(w http.ResponseWriter, code int, format string, args ...any) {
	JSON(w, code, map[string]string{"error": fmt.Sprintf(format, args...)})
}

// WriteWSError reports a failed WebSocket handshake as a normal JSON response,
// which lets the browser surface a readable reason instead of an opaque 400.
func WriteWSError(w http.ResponseWriter, status int, format string, args ...any) {
	Error(w, status, format, args...)
}

// BearerToken extracts a token from the Authorization header, ?token= or a cookie.
func BearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	if c, err := r.Cookie("np_token"); err == nil {
		return c.Value
	}
	return ""
}

// ClientIP returns the best-effort remote address of a request.
func ClientIP(r *http.Request) string {
	if v := r.Header.Get("CF-Connecting-IP"); v != "" {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.IndexByte(v, ','); i > 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// NormalizeHost strips scheme, path and port from an address so we can rebuild a URL.
func NormalizeHost(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSuffix(s, "/")
}

// BaseURL builds the base URL used to reach an agent.
//
// Supports the three deployment styles this project targets:
//   - direct IP + port:      1.2.3.4 + 8899          -> http://1.2.3.4:8899
//   - domain + port:         srv.example.com + 8899  -> http://srv.example.com:8899
//   - cloudflared tunnel:    x.trycloudflare.com (no port) -> https://x.trycloudflare.com
func BaseURL(host string, port int, scheme string) string {
	host = NormalizeHost(host)
	if host == "" {
		return ""
	}
	// A tunnel / reverse-proxied address already carries its own scheme.
	tunnel := port <= 0 || port == 80 || port == 443
	if tunnel && (strings.Contains(host, "trycloudflare.com") || strings.Contains(host, "cfargotunnel.com")) {
		scheme = "https"
	}
	if scheme == "" {
		if port == 443 {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	// Host already contains an explicit port.
	if _, _, err := net.SplitHostPort(host); err == nil {
		return scheme + "://" + host
	}
	if port <= 0 || (port == 80 && scheme == "http") || (port == 443 && scheme == "https") {
		return scheme + "://" + host
	}
	return fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(host, strconv.Itoa(port)))
}

// EnsureDir creates a directory with 0700 permissions.
func EnsureDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

// DefaultStateDir returns the per-user state directory for the panel.
func DefaultStateDir() string {
	if v := os.Getenv("NODEPANEL_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".nodepanel")
}

// WriteFileAtomic writes data to path without leaving a partial file behind.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Bytes formats a byte count in a human friendly way.
func Bytes(v float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return strconv.FormatFloat(v, 'f', 0, 64) + units[i]
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + units[i]
}

// Rate formats a per-second byte rate.
func Rate(v float64) string { return Bytes(v) + "/s" }

// Duration formats a number of seconds as a compact uptime string.
func Duration(sec float64) string {
	if sec <= 0 {
		return "-"
	}
	d := int64(sec)
	day := d / 86400
	hour := (d % 86400) / 3600
	min := (d % 3600) / 60
	switch {
	case day > 0:
		return fmt.Sprintf("%dd %dh %dm", day, hour, min)
	case hour > 0:
		return fmt.Sprintf("%dh %dm", hour, min)
	case min > 0:
		return fmt.Sprintf("%dm %ds", min, d%60)
	default:
		return fmt.Sprintf("%ds", d)
	}
}

// Percent renders a 0-100 value.
func Percent(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) + "%" }

// Round1 rounds to one decimal place to keep payloads small.
func Round1(v float64) float64 {
	if v != v { // NaN
		return 0
	}
	if v > 1e15 || v < -1e15 {
		return v
	}
	return float64(int64(v*10+sign(v)*0.5)) / 10
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

// NowMS returns the current unix time in milliseconds.
func NowMS() int64 { return time.Now().UnixNano() / int64(time.Millisecond) }

// CopyFile copies src to dst with the given mode.
func CopyFile(src, dst string, mode os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return WriteFileAtomic(dst, b, mode)
}
