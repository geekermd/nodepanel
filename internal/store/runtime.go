package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/geekermd/nodepanel/internal/shared"
)

// RuntimeFile is the persisted subset of the panel-side runtime rollups. It
// keeps 访问量 (access counters) and traffic totals across panel restarts.
type RuntimeFile struct {
	ID          string               `json:"id"`
	Requests    int64                `json:"requests"`
	RequestsDay int64                `json:"requests_today"`
	Day         string               `json:"day"`
	Days        map[string]*DayUsage `json:"days"`
	RxTotal     float64              `json:"rx_total"`
	TxTotal     float64              `json:"tx_total"`
	UpdatedAt   int64                `json:"updated_at"`
}

// DayUsage mirrors the panel rollup so it can be persisted without an import
// cycle.
type DayUsage struct {
	Requests int64   `json:"requests"`
	Rx       float64 `json:"rx"`
	Tx       float64 `json:"tx"`
}

// runtimePath is where a node's rollups live.
func (s *Store) runtimePath(id string) string {
	return filepath.Join(s.dir, "series", id, "runtime.json")
}

// LoadRuntime reads the persisted rollups for one node.
func (s *Store) LoadRuntime(id string) (*RuntimeFile, error) {
	b, err := os.ReadFile(s.runtimePath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return &RuntimeFile{ID: id, Days: map[string]*DayUsage{}}, nil
		}
		return nil, err
	}
	var out RuntimeFile
	if err := json.Unmarshal(b, &out); err != nil {
		return &RuntimeFile{ID: id, Days: map[string]*DayUsage{}}, nil
	}
	if out.Days == nil {
		out.Days = map[string]*DayUsage{}
	}
	return &out, nil
}

// WriteRuntimeFile persists a rollup file.
func (s *Store) WriteRuntimeFile(rf *RuntimeFile) error {
	if rf == nil || rf.ID == "" {
		return nil
	}
	rf.UpdatedAt = time.Now().Unix()
	b, err := json.Marshal(rf)
	if err != nil {
		return err
	}
	path := s.runtimePath(rf.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return shared.WriteFileAtomic(path, b, 0o600)
}
