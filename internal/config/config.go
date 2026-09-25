package config

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Hannah    HannahConfig    `yaml:"hannah"`
	Server    ServerConfig    `yaml:"server"`
	DB        DBConfig        `yaml:"db"`
	Retention RetentionConfig `yaml:"retention"`
	Log       LogConfig       `yaml:"log"`
}

// HannahConfig holds the connection parameters for Hannah's gRPC endpoint.
type HannahConfig struct {
	Address string `yaml:"address"`
}

// ServerConfig describes the collector's own gRPC server (LogService) and how it
// announces itself to Hannah Core via LogCollectorConnect.
type ServerConfig struct {
	Listen string `yaml:"listen"`
	// Host announced to Hannah. Empty = Hannah uses the address the collector connected from.
	AdvertiseHost string `yaml:"advertise_host"`
	// Port announced to Hannah. 0 = the port from Listen.
	AdvertisePort int `yaml:"advertise_port"`
	// Distinguishes several collectors; a second one with the same name replaces the first.
	Instance string `yaml:"instance"`
}

type DBConfig struct {
	Path string `yaml:"path"`
}

// RetentionConfig bounds what the collector keeps — whichever limit is hit first wins.
type RetentionConfig struct {
	Days      int `yaml:"days"`
	MaxSizeMB int `yaml:"max_size_mb"`
}

type LogConfig struct {
	Level string `yaml:"level"`
}

func Load(path string) (*Config, error) {
	cfg := defaults()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading config file: %w", err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parsing config file: %w", err)
		}
	}

	if err := applyEnv(cfg); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// EffectiveAdvertisePort is the port announced to Hannah: AdvertisePort, or the port from Listen.
func (c *Config) EffectiveAdvertisePort() int {
	if c.Server.AdvertisePort != 0 {
		return c.Server.AdvertisePort
	}
	_, port, err := net.SplitHostPort(c.Server.Listen)
	if err != nil {
		return 0
	}
	p, _ := strconv.Atoi(port)
	return p
}

func defaults() *Config {
	return &Config{
		Hannah: HannahConfig{
			Address: "localhost:50051",
		},
		Server: ServerConfig{
			Listen:   ":50060",
			Instance: "default",
		},
		DB: DBConfig{
			Path: "logs.db",
		},
		Retention: RetentionConfig{
			Days:      7,
			MaxSizeMB: 256,
		},
		Log: LogConfig{
			Level: "info",
		},
	}
}

func (c *Config) validate() error {
	if _, _, err := net.SplitHostPort(c.Server.Listen); err != nil {
		return fmt.Errorf("server.listen %q: %w", c.Server.Listen, err)
	}
	if c.Retention.Days <= 0 {
		return fmt.Errorf("retention.days must be > 0, got %d", c.Retention.Days)
	}
	if c.Retention.MaxSizeMB <= 0 {
		return fmt.Errorf("retention.max_size_mb must be > 0, got %d", c.Retention.MaxSizeMB)
	}
	return nil
}

func applyEnv(cfg *Config) error {
	if v := os.Getenv("HANNAH_LOGCOLLECTOR_HANNAH_ADDRESS"); v != "" {
		cfg.Hannah.Address = v
	}
	if v := os.Getenv("HANNAH_LOGCOLLECTOR_SERVER_LISTEN"); v != "" {
		cfg.Server.Listen = v
	}
	if v := os.Getenv("HANNAH_LOGCOLLECTOR_SERVER_ADVERTISE_HOST"); v != "" {
		cfg.Server.AdvertiseHost = v
	}
	if v := os.Getenv("HANNAH_LOGCOLLECTOR_SERVER_INSTANCE"); v != "" {
		cfg.Server.Instance = v
	}
	if v := os.Getenv("HANNAH_LOGCOLLECTOR_DB_PATH"); v != "" {
		cfg.DB.Path = v
	}
	if v := os.Getenv("HANNAH_LOGCOLLECTOR_LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
	for key, target := range map[string]*int{
		"HANNAH_LOGCOLLECTOR_SERVER_ADVERTISE_PORT": &cfg.Server.AdvertisePort,
		"HANNAH_LOGCOLLECTOR_RETENTION_DAYS":        &cfg.Retention.Days,
		"HANNAH_LOGCOLLECTOR_RETENTION_MAX_SIZE_MB": &cfg.Retention.MaxSizeMB,
	} {
		v := os.Getenv(key)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%s: %q is not a number", key, v)
		}
		*target = n
	}
	return nil
}
