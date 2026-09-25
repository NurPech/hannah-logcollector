package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaults(t *testing.T) {
	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, "localhost:50051", cfg.Hannah.Address)
	assert.Equal(t, ":50060", cfg.Server.Listen)
	assert.Equal(t, "default", cfg.Server.Instance)
	assert.Equal(t, 7, cfg.Retention.Days)
	assert.Equal(t, 256, cfg.Retention.MaxSizeMB)
	assert.Equal(t, 50060, cfg.EffectiveAdvertisePort())
}

func TestFileAndEnvOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
hannah:
  address: "hannah:50051"
retention:
  days: 3
`), 0o600))

	t.Setenv("HANNAH_LOGCOLLECTOR_RETENTION_MAX_SIZE_MB", "64")
	t.Setenv("HANNAH_LOGCOLLECTOR_SERVER_ADVERTISE_PORT", "6000")

	cfg, err := Load(path)
	require.NoError(t, err)

	assert.Equal(t, "hannah:50051", cfg.Hannah.Address)
	assert.Equal(t, 3, cfg.Retention.Days)
	assert.Equal(t, 64, cfg.Retention.MaxSizeMB)
	assert.Equal(t, 6000, cfg.EffectiveAdvertisePort())
}

func TestInvalidValuesRejected(t *testing.T) {
	t.Setenv("HANNAH_LOGCOLLECTOR_RETENTION_DAYS", "abc")
	_, err := Load("")
	assert.Error(t, err)

	t.Setenv("HANNAH_LOGCOLLECTOR_RETENTION_DAYS", "0")
	_, err = Load("")
	assert.Error(t, err)
}
