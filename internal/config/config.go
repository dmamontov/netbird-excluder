// Package config persists the domain blacklist and enabled/disabled state so
// they can be managed via CLI commands instead of process arguments, and
// picked up live by an already-running daemon.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const Path = "/etc/netbird-excluder/config.json"

type Config struct {
	Domains []string `json:"domains"`
	Enabled bool     `json:"enabled"`
}

// Load reads the config file, returning a default (enabled, no domains)
// config if it doesn't exist yet.
func Load() (*Config, error) {
	data, err := os.ReadFile(Path)
	if os.IsNotExist(err) {
		return &Config{Enabled: true}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func Save(c *Config) error {
	if err := os.MkdirAll(filepath.Dir(Path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(Path, data, 0644)
}

// Add appends any of domains not already present, returning the ones added.
func (c *Config) Add(domains []string) []string {
	existing := make(map[string]bool, len(c.Domains))
	for _, d := range c.Domains {
		existing[d] = true
	}

	var added []string
	for _, d := range domains {
		if !existing[d] {
			c.Domains = append(c.Domains, d)
			existing[d] = true
			added = append(added, d)
		}
	}
	return added
}

// Remove drops any of domains that are present, returning the ones removed.
func (c *Config) Remove(domains []string) []string {
	toRemove := make(map[string]bool, len(domains))
	for _, d := range domains {
		toRemove[d] = true
	}

	var removed, kept []string
	for _, d := range c.Domains {
		if toRemove[d] {
			removed = append(removed, d)
		} else {
			kept = append(kept, d)
		}
	}
	c.Domains = kept
	return removed
}
