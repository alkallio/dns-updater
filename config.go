package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Config holds the application configuration
type Config struct {
	Domains []DomainConfig `json:"domains"`
	mu      sync.RWMutex
}

// NewConfig creates a new Config instance
func NewConfig() *Config {
	return &Config{
		Domains: []DomainConfig{},
	}
}

// LoadConfig loads configuration from a JSON file
func LoadConfig(path string) (*Config, error) {
	config := NewConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Config file doesn't exist, return empty config
			return config, nil
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	if len(data) == 0 {
		// Empty file, return empty config
		return config, nil
	}

	if err := json.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return config, nil
}

// SaveConfig saves configuration to a JSON file
func (c *Config) SaveConfig(path string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// GetDomains returns a copy of the domain configurations
func (c *Config) GetDomains() []DomainConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()

	domains := make([]DomainConfig, len(c.Domains))
	copy(domains, c.Domains)
	return domains
}

// SetDomains replaces all domain configurations
func (c *Config) SetDomains(domains []DomainConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.Domains = domains
}

// AddDomain adds a new domain configuration
func (c *Config) AddDomain(domain DomainConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.Domains = append(c.Domains, domain)
}

// RemoveDomain removes a domain configuration by record name
func (c *Config) RemoveDomain(recordName string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i, d := range c.Domains {
		if d.RecordName == recordName {
			c.Domains = append(c.Domains[:i], c.Domains[i+1:]...)
			return true
		}
	}
	return false
}

// UpdateDomain updates a domain configuration by record name
func (c *Config) UpdateDomain(recordName string, updated DomainConfig) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i, d := range c.Domains {
		if d.RecordName == recordName {
			c.Domains[i] = updated
			return true
		}
	}
	return false
}
