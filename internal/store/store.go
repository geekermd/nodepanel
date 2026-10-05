// Package store is the panel's local persistent state: the node inventory,
// per-node time series and the TODO list. It deliberately avoids SQLite
// (CGO-free single binary) and instead keeps everything in memory with an
// append-only JSONL journal per node.
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/geekermd/nodepanel/internal/shared"
)

// NodeModes mirror the three ways a node can be reached.
const (
	ModeDirect = "direct" // IP/域名 + 端口
	ModeDomain = "domain" // 域名 (80/443, 反向代理)
	ModeTunnel = "tunnel" // cloudflared 内网穿透地址
)

// Node is one managed server.
type Node struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Scheme   string   `json:"scheme"` // http | https
	Mode     string   `json:"mode"`
	Token    string   `json:"token"`
	Note     string   `json:"note"`
	Group    string   `json:"group"`
	Tags     []string `json:"tags"`
	SSHUser  string   `json:"ssh_user"`
	SSHPort  int      `json:"ssh_port"`
	SSHKey   string   `json:"ssh_key"`   // optional: PEM private key
	SSHPass  string   `json:"ssh_pass"`  // optional: remembered password (local only)
	Remember bool     `json:"remember"`  // store the SSH password on disk
	UseRelay bool     `json:"use_relay"` // tunnel SSH through the agent
	Interval int      `json:"interval"`  // polling interval, seconds
	Enabled  bool     `json:"enabled"`

	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// Todo is one checklist entry.
type Todo struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Done      bool   `json:"done"`
	NodeID    string `json:"node_id"`
	Priority  int    `json:"priority"` // 0 normal, 1 high
	DueAt     int64  `json:"due_at"`
	CreatedAt int64  `json:"created_at"`
	DoneAt    int64  `json:"done_at"`
}

// Settings holds panel-level configuration that is editable from the UI.
type Settings struct {
	Listen       string  `json:"listen"`
	PasswordHash string  `json:"password_hash"`
	PollSeconds  int     `json:"poll_seconds"`
	OfflineEvery int     `json:"offline_seconds"`
	KeepDays     int     `json:"keep_days"`
	AlertCPU     float64 `json:"alert_cpu"`
	AlertMem     float64 `json:"alert_mem"`
	AlertDisk    float64 `json:"alert_disk"`
	Theme        string  `json:"theme"`
}

// Store is the whole panel state.
type Store struct {
	mu   sync.RWMutex
	dir  string
	file string

	Nodes    []*Node  `json:"nodes"`
	Todos    []*Todo  `json:"todos"`
	Settings Settings `json:"settings"`
	Secret   string   `json:"secret"` // session signing key
	SSHKey   string   `json:"ssh_key"`

	series map[string]*Series
}

// DefaultSettings returns sane defaults.
func DefaultSettings() Settings {
	return Settings{
		Listen:       "127.0.0.1:8787",
		PollSeconds:  5,
		OfflineEvery: 15,
		KeepDays:     30,
		AlertCPU:     90,
		AlertMem:     90,
		AlertDisk:    90,
		Theme:        "dark",
	}
}

// Open loads (or creates) the panel state below dir.
func Open(dir string) (*Store, error) {
	if err := shared.EnsureDir(dir); err != nil {
		return nil, err
	}
	s := &Store{
		dir:      dir,
		file:     filepath.Join(dir, "panel.json"),
		Settings: DefaultSettings(),
		series:   map[string]*Series{},
	}
	if b, err := os.ReadFile(s.file); err == nil {
		// Load into a temporary value so a broken file cannot half-apply.
		tmp := struct {
			Nodes    []*Node  `json:"nodes"`
			Todos    []*Todo  `json:"todos"`
			Settings Settings `json:"settings"`
			Secret   string   `json:"secret"`
			SSHKey   string   `json:"ssh_key"`
		}{Settings: s.Settings}
		if err := json.Unmarshal(b, &tmp); err != nil {
			return nil, err
		}
		s.Nodes, s.Todos, s.Secret, s.SSHKey = tmp.Nodes, tmp.Todos, tmp.Secret, tmp.SSHKey
		if tmp.Settings.Listen != "" || tmp.Settings.PollSeconds != 0 {
			s.Settings = tmp.Settings
		}
		if s.Settings.PollSeconds <= 0 {
			s.Settings.PollSeconds = 5
		}
		if s.Settings.OfflineEvery <= 0 {
			s.Settings.OfflineEvery = 15
		}
	}
	if s.Secret == "" {
		s.Secret = shared.RandomToken(32)
	}
	for _, n := range s.Nodes {
		if n.ID == "" {
			n.ID = shared.RandomToken(8)
		}
		if n.Scheme == "" {
			n.Scheme = "http"
		}
		if n.SSHPort == 0 {
			n.SSHPort = 22
		}
		if n.SSHUser == "" {
			n.SSHUser = "root"
		}
		if n.Interval == 0 {
			n.Interval = s.Settings.PollSeconds
		}
	}
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// Dir returns the state directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return shared.WriteFileAtomic(s.file, b, 0o600)
}

// Save flushes the panel state.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// Series returns (creating on demand) the time series of a node.
func (s *Store) Series(nodeID string) *Series {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seriesLocked(nodeID)
}

func (s *Store) seriesLocked(nodeID string) *Series {
	if ser, ok := s.series[nodeID]; ok {
		return ser
	}
	ser := NewSeries(filepath.Join(s.dir, "series", nodeID), nodeID)
	_ = ser.Load()
	s.series[nodeID] = ser
	return ser
}

// ListNodes returns a copy of the node list.
func (s *Store) ListNodes() []*Node {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Node, len(s.Nodes))
	copy(out, s.Nodes)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// Node returns one node by id.
func (s *Store) Node(id string) *Node {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, n := range s.Nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// AddNode inserts a node, allocating an id.
func (s *Store) AddNode(n *Node) (*Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n.ID == "" {
		n.ID = shared.RandomToken(8)
	}
	n.CreatedAt = time.Now().Unix()
	n.UpdatedAt = n.CreatedAt
	if n.Interval <= 0 {
		n.Interval = s.Settings.PollSeconds
	}
	if n.SSHPort == 0 {
		n.SSHPort = 22
	}
	if n.SSHUser == "" {
		n.SSHUser = "root"
	}
	if n.Scheme == "" {
		n.Scheme = "http"
	}
	if n.Mode == "" {
		n.Mode = ModeDirect
	}
	n.Enabled = true
	s.Nodes = append(s.Nodes, n)
	return n, s.saveLocked()
}

// UpdateNode applies fn to a node and persists the result.
func (s *Store) UpdateNode(id string, fn func(*Node)) (*Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.Nodes {
		if n.ID == id {
			fn(n)
			n.UpdatedAt = time.Now().Unix()
			return n, s.saveLocked()
		}
	}
	return nil, os.ErrNotExist
}

// RemoveNode deletes a node and its history.
func (s *Store) RemoveNode(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.Nodes[:0]
	for _, n := range s.Nodes {
		if n.ID != id {
			out = append(out, n)
		}
	}
	s.Nodes = out
	if ser, ok := s.series[id]; ok {
		_ = ser.Purge()
		delete(s.series, id)
	}
	todos := s.Todos[:0]
	for _, t := range s.Todos {
		if t.NodeID != id {
			todos = append(todos, t)
		}
	}
	s.Todos = todos
	return s.saveLocked()
}

// ---------------------------------------------------------------------------
// TODO list
// ---------------------------------------------------------------------------

// ListTodos returns all todos, unfinished first.
func (s *Store) ListTodos() []*Todo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Todo, len(s.Todos))
	copy(out, s.Todos)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Done != out[j].Done {
			return !out[i].Done
		}
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out
}

// AddTodo appends a todo item.
func (s *Store) AddTodo(t *Todo) (*Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.ID == "" {
		t.ID = shared.RandomToken(6)
	}
	t.CreatedAt = time.Now().Unix()
	if t.Text == "" {
		return nil, os.ErrInvalid
	}
	s.Todos = append(s.Todos, t)
	return t, s.saveLocked()
}

// UpdateTodo mutates a todo item.
func (s *Store) UpdateTodo(id string, fn func(*Todo)) (*Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.Todos {
		if t.ID == id {
			fn(t)
			if t.Done && t.DoneAt == 0 {
				t.DoneAt = time.Now().Unix()
			}
			if !t.Done {
				t.DoneAt = 0
			}
			return t, s.saveLocked()
		}
	}
	return nil, os.ErrNotExist
}

// RemoveTodo deletes a todo item.
func (s *Store) RemoveTodo(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.Todos[:0]
	for _, t := range s.Todos {
		if t.ID != id {
			out = append(out, t)
		}
	}
	s.Todos = out
	return s.saveLocked()
}

// UpdateSettings mutates panel settings.
func (s *Store) UpdateSettings(fn func(*Settings)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.Settings)
	return s.saveLocked()
}

// GetSettings returns a copy of the settings.
func (s *Store) GetSettings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Settings
}
