package config

import (
	"os"
	"strings"
)

// Config is loaded from environment variables.
type Config struct {
	ListenAddr         string
	DBPath             string
	JWTSecret          string
	CalibreLibraryPath string
	// CalibredbBin is optional override for the calibredb executable path.
	CalibredbBin string
}

// Load reads configuration from the environment with documented defaults.
func Load() Config {
	return Config{
		ListenAddr:         envOr("LISTEN_ADDR", ":8090"),
		DBPath:             envOr("DB_PATH", "./data/folio.db"),
		JWTSecret:          envOr("JWT_SECRET", "change-me-in-production"),
		CalibreLibraryPath: strings.TrimSpace(os.Getenv("CALIBRE_LIBRARY_PATH")),
		CalibredbBin:       envOr("CALIBREDB_BIN", "calibredb"),
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
