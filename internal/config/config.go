package config

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Source    SynapseConfig   `yaml:"source"`
	Target    DendriteConfig  `yaml:"target"`
	Migration MigrationConfig `yaml:"migration"`
}

type SynapseConfig struct {
	BaseURL            string `yaml:"base_url"`
	ServerName         string `yaml:"server_name"`
	AccessToken        string `yaml:"access_token"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
}

type DendriteConfig struct {
	BaseURL                  string `yaml:"base_url"`
	ServerName               string `yaml:"server_name"`
	AccessToken              string `yaml:"access_token"`
	RegistrationSharedSecret string `yaml:"registration_shared_secret"`
	InsecureSkipVerify       bool   `yaml:"insecure_skip_verify"`
}

type MigrationConfig struct {
	StatePath          string `yaml:"state_path"`
	PasswordReportPath string `yaml:"password_report_path"`
	Concurrency        int    `yaml:"concurrency"`
	TempPasswordPrefix string `yaml:"temp_password_prefix"`
	UserLimit          int    `yaml:"user_limit"`
	RoomLimit          int    `yaml:"room_limit"`
	MediaLimit         int    `yaml:"media_limit"`
}

func Default() Config {
	return Config{
		Source: SynapseConfig{
			BaseURL:    "https://synapse.example.com",
			ServerName: "matrix.example.com",
		},
		Target: DendriteConfig{
			BaseURL:    "https://dendrite.example.com",
			ServerName: "matrix.example.com",
		},
		Migration: MigrationConfig{
			StatePath:          "migration_state.json",
			PasswordReportPath: "temp_passwords.csv",
			Concurrency:        4,
			TempPasswordPrefix: "reset-me-",
		},
	}
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := Default()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func WriteExample(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("config already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat config: %w", err)
	}

	data, err := yaml.Marshal(Default())
	if err != nil {
		return fmt.Errorf("marshal example config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func (c Config) Validate() error {
	if c.Source.BaseURL == "" {
		return errors.New("source.base_url is required")
	}
	if c.Source.ServerName == "" {
		return errors.New("source.server_name is required")
	}
	if c.Source.AccessToken == "" {
		return errors.New("source.access_token is required")
	}
	if c.Target.BaseURL == "" {
		return errors.New("target.base_url is required")
	}
	if c.Target.ServerName == "" {
		return errors.New("target.server_name is required")
	}
	if c.Target.AccessToken == "" {
		return errors.New("target.access_token is required")
	}
	if c.Target.RegistrationSharedSecret == "" {
		return errors.New("target.registration_shared_secret is required")
	}
	if c.Migration.StatePath == "" {
		return errors.New("migration.state_path is required")
	}
	if c.Migration.Concurrency <= 0 {
		c.Migration.Concurrency = 1
	}
	if c.Migration.PasswordReportPath == "" {
		c.Migration.PasswordReportPath = "temp_passwords.csv"
	}
	if c.Migration.TempPasswordPrefix == "" {
		c.Migration.TempPasswordPrefix = "reset-me-"
	}
	return nil
}
