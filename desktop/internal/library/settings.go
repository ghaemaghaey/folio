package library

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// DefaultOPDSBaseURL is the built-in Calibre-Web instance when the user has not set one.
const DefaultOPDSBaseURL = "https://calibre.ghaemghh.ir"

// Settings holds user preferences (OPDS, etc.).
type Settings struct {
	OPDSBaseURL  string `json:"opdsBaseURL"`
	OPDSUsername string `json:"opdsUsername"`
	OPDSPassword string `json:"opdsPassword"` // stored locally; Basic Auth later
}

// EffectiveOPDSBaseURL returns the configured URL or the built-in default.
func (s Settings) EffectiveOPDSBaseURL() string {
	u := strings.TrimRight(strings.TrimSpace(s.OPDSBaseURL), "/")
	if u == "" {
		return DefaultOPDSBaseURL
	}
	return u
}

// SettingsStore is a JSON file under ~/.folio/settings.json.
type SettingsStore struct {
	path string
	mu   sync.Mutex
	data Settings
}

// DefaultSettingsPath returns ~/.folio/settings.json.
func DefaultSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".folio")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

// OpenSettings loads or creates settings.
func OpenSettings(path string) (*SettingsStore, error) {
	s := &SettingsStore{
		path: path,
		data: Settings{OPDSBaseURL: DefaultOPDSBaseURL},
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, s.Save()
		}
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, fmt.Errorf("parse settings: %w", err)
		}
	}
	// Empty URL → built-in default so first launch is already connected.
	if strings.TrimSpace(s.data.OPDSBaseURL) == "" {
		s.data.OPDSBaseURL = DefaultOPDSBaseURL
	}
	return s, nil
}

// Get returns a copy of settings (with default OPDS URL if unset).
func (s *SettingsStore) Get() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.data
	if strings.TrimSpace(out.OPDSBaseURL) == "" {
		out.OPDSBaseURL = DefaultOPDSBaseURL
	}
	return out
}

// Update replaces settings and saves.
func (s *SettingsStore) Update(next Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next.OPDSBaseURL = strings.TrimRight(strings.TrimSpace(next.OPDSBaseURL), "/")
	s.data = next
	return s.saveLocked()
}

// Save writes settings.
func (s *SettingsStore) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *SettingsStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// BooksDir returns <executable-dir>/books, creating it if needed.
func BooksDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	// Resolve symlinks when possible so we land next to the real binary.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Join(filepath.Dir(exe), "books")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// SanitizeFilename keeps a readable title-safe filename stem.
func SanitizeFilename(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "book"
	}
	var b strings.Builder
	for _, r := range title {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_' || r == '.':
			b.WriteRune('-')
		default:
			// Keep letters from other scripts (Persian, etc.)
			if r > 127 {
				b.WriteRune(r)
			}
		}
	}
	out := strings.Trim(b.String(), "-.")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	if out == "" {
		out = "book"
	}
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}
