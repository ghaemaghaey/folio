package library

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// OPDSRecord maps a remote OPDS entry to a local file after download.
type OPDSRecord struct {
	OPDSID      string `json:"opdsId"`
	Title       string `json:"title"`
	LocalPath   string `json:"localPath"`
	LocalBookID string `json:"localBookId"`
	Fingerprint string `json:"fingerprint"` // first 64KiB SHA-256 (same as Book)
	ContentHash string `json:"contentHash"` // full-file SHA-256
	Size        int64  `json:"size"`
	Format      string `json:"format"`
}

// OPDSIndex persists remote→local ownership mapping.
type OPDSIndex struct {
	path string
	mu   sync.Mutex
	data opdsIndexFile
}

type opdsIndexFile struct {
	Version int                   `json:"version"`
	Entries map[string]OPDSRecord `json:"entries"` // key = OPDS id
}

// DefaultOPDSIndexPath returns ~/.folio/opds-index.json.
func DefaultOPDSIndexPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".folio")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "opds-index.json"), nil
}

// OpenOPDSIndex loads or creates the index.
func OpenOPDSIndex(path string) (*OPDSIndex, error) {
	idx := &OPDSIndex{
		path: path,
		data: opdsIndexFile{Version: 1, Entries: map[string]OPDSRecord{}},
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return idx, idx.Save()
		}
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &idx.data); err != nil {
			return nil, fmt.Errorf("parse opds index: %w", err)
		}
	}
	if idx.data.Entries == nil {
		idx.data.Entries = map[string]OPDSRecord{}
	}
	return idx, nil
}

// Get returns a record by OPDS id.
func (idx *OPDSIndex) Get(opdsID string) (OPDSRecord, bool) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	r, ok := idx.data.Entries[opdsID]
	return r, ok
}

// Put stores/updates a record.
func (idx *OPDSIndex) Put(rec OPDSRecord) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if rec.OPDSID == "" {
		return fmt.Errorf("empty opds id")
	}
	idx.data.Entries[rec.OPDSID] = rec
	return idx.saveLocked()
}

// FindByContentHash finds any record with the same full-file hash.
func (idx *OPDSIndex) FindByContentHash(hash string) (OPDSRecord, bool) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if hash == "" {
		return OPDSRecord{}, false
	}
	for _, r := range idx.data.Entries {
		if r.ContentHash == hash {
			return r, true
		}
	}
	return OPDSRecord{}, false
}

// FindByFingerprint finds any record with the same fingerprint.
func (idx *OPDSIndex) FindByFingerprint(fp string) (OPDSRecord, bool) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if fp == "" {
		return OPDSRecord{}, false
	}
	for _, r := range idx.data.Entries {
		if r.Fingerprint == fp {
			return r, true
		}
	}
	return OPDSRecord{}, false
}

// All returns a copy of all records.
func (idx *OPDSIndex) All() map[string]OPDSRecord {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	out := make(map[string]OPDSRecord, len(idx.data.Entries))
	for k, v := range idx.data.Entries {
		out[k] = v
	}
	return out
}

// Save writes the index.
func (idx *OPDSIndex) Save() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.saveLocked()
}

func (idx *OPDSIndex) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(idx.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(idx.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := idx.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, idx.path)
}
