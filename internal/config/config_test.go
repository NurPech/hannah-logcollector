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
	assert.Empty(t, cfg.Syslog.Listen, "the syslog receiver is off by default")
}

func TestSyslogListen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("syslog:\n  listen: \":5514\"\n"), 0o600))
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, ":5514", cfg.Syslog.Listen)

	t.Setenv("HANNAH_LOGCOLLECTOR_SYSLOG_LISTEN", "127.0.0.1:6514")
	cfg, err = Load(path)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:6514", cfg.Syslog.Listen)

	t.Setenv("HANNAH_LOGCOLLECTOR_SYSLOG_LISTEN", "5514")
	_, err = Load(path)
	assert.Error(t, err, "an address without a port is rejected")
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

func TestForward(t *testing.T) {
	cfg, err := Load("")
	require.NoError(t, err)
	assert.Empty(t, cfg.Forward.Address, "forwarding is off by default")
	assert.Equal(t, "tcp", cfg.Forward.Protocol)

	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("forward:\n  address: \"alloy:51400\"\n  protocol: udp\n"), 0o600))
	cfg, err = Load(path)
	require.NoError(t, err)
	assert.Equal(t, "alloy:51400", cfg.Forward.Address)
	assert.Equal(t, "udp", cfg.Forward.Protocol)

	t.Setenv("HANNAH_LOGCOLLECTOR_FORWARD_ADDRESS", "10.0.0.7:514")
	t.Setenv("HANNAH_LOGCOLLECTOR_FORWARD_PROTOCOL", "tcp")
	cfg, err = Load(path)
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.7:514", cfg.Forward.Address)
	assert.Equal(t, "tcp", cfg.Forward.Protocol)
}

func TestInvalidForwardRejected(t *testing.T) {
	t.Setenv("HANNAH_LOGCOLLECTOR_FORWARD_PROTOCOL", "tls")
	_, err := Load("")
	assert.Error(t, err, "only tcp and udp")

	t.Setenv("HANNAH_LOGCOLLECTOR_FORWARD_PROTOCOL", "tcp")
	t.Setenv("HANNAH_LOGCOLLECTOR_FORWARD_ADDRESS", "alloy")
	_, err = Load("")
	assert.Error(t, err, "an address without a port")
}
