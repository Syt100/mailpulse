package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type Config struct {
	DataDir     string `json:"data_dir"`
	KeyFile     string `json:"key_file"`
	Socket      string `json:"socket"`
	Listen      string `json:"listen"`
	LogLevel    string `json:"log_level"`
	Capacity    int    `json:"capacity"`
	PollSeconds int    `json:"poll_seconds"`
}

func Default() Config {
	c := Config{DataDir: os.Getenv("MAILPULSE_DATA_DIR"), KeyFile: os.Getenv("MAILPULSE_KEY_FILE"), Socket: os.Getenv("MAILPULSE_SOCKET"), Listen: os.Getenv("MAILPULSE_LISTEN"), Capacity: 10000, PollSeconds: 60}
	if c.DataDir == "" {
		c.DataDir = "/data"
	}
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	c.LogLevel = os.Getenv("MAILPULSE_LOG_LEVEL")
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	return c
}
func Load(path string) (Config, error) {
	c := Default()
	if path != "" {
		b, e := os.ReadFile(path)
		if e != nil {
			return c, fmt.Errorf("configuration unavailable")
		}
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		if e = d.Decode(&c); e != nil {
			return c, fmt.Errorf("invalid configuration")
		}
		if e = d.Decode(new(any)); e != io.EOF {
			return c, fmt.Errorf("invalid configuration")
		}
	}
	return c, nil
}
func (c *Config) Finalize() error {
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid log level")
	}
	if c.DataDir == "" || c.Capacity < 1 || c.PollSeconds < 1 {
		return fmt.Errorf("invalid runtime settings")
	}
	if c.Socket == "" {
		c.Socket = filepath.Join(c.DataDir, "control.sock")
	}
	if c.KeyFile == "" {
		c.KeyFile = filepath.Join(c.DataDir, "master.key")
	}
	if len(c.Socket) > 103 {
		return fmt.Errorf("Unix socket path is too long")
	}
	return nil
}
func (c Config) Database() string { return filepath.Join(c.DataDir, "mailpulse.db") }
