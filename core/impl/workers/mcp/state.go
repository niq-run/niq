package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ServerStore persists the authoritative set of mcp servers across restarts.
// Mirrors the external-worker state idiom (atomic temp-file + rename write);
// injectable for tests.
type ServerStore interface {
	Load() ([]ServerConfig, error)
	Save(servers []ServerConfig) error
}

// FileServerStore atomically persists servers to a JSON file.
type FileServerStore struct {
	path string
}

// NewFileServerStore creates a store writing to path.
func NewFileServerStore(path string) *FileServerStore { return &FileServerStore{path: path} }

func (s *FileServerStore) Load() ([]ServerConfig, error) {
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var servers []ServerConfig
	if err := json.Unmarshal(b, &servers); err != nil {
		return nil, err
	}
	return servers, nil
}

func (s *FileServerStore) Save(servers []ServerConfig) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}