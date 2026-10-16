// Package config loads zhipucode2api service configuration from environment
// variables, preserving the names and default values frozen by the
// env contract for the Go rewrite of the legacy zcode2api service.
//
// The five contract variables and their defaults (task t3 contract, to be
// cross-checked against docs/contract/env-contract.md when it lands):
//
//	ZCODE_PORT      int      default 3000     (production overrides via .env; live listener is 127.0.0.1:9120)
//	ZCODE_HOST      string   default 0.0.0.0
//	ZCODE_DATA_DIR  string   default data
//	ZCODE_ADMIN_KEY string   default change-me (bootstrap value; DB meta "admin_key" may override at runtime)
//	ZCODE_NODE_PATH string   default node
//
// An empty value behaves like an unset variable (the default applies). This
// is a deliberate compatibility choice of the rewrite; the legacy Python
// service's exact behaviour for empty-but-set variables is unverified and is
// left for the frozen contract to adjudicate.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Environment variable names.
const (
	EnvPort     = "ZCODE_PORT"
	EnvHost     = "ZCODE_HOST"
	EnvDataDir  = "ZCODE_DATA_DIR"
	EnvAdminKey = "ZCODE_ADMIN_KEY"
	EnvNodePath = "ZCODE_NODE_PATH"
)

// Default values used when the corresponding variable is unset or empty.
const (
	DefaultPort     = 3000
	DefaultHost     = "0.0.0.0"
	DefaultDataDir  = "data"
	DefaultAdminKey = "change-me"
	DefaultNodePath = "node"
)

// Config is the resolved service configuration.
type Config struct {
	Port     int    // TCP port the gateway listens on
	Host     string // bind address
	DataDir  string // directory holding accounts.db (and related data files)
	AdminKey string // admin API key bootstrap value (never log this)
	NodePath string // node binary used for captcha solving
}

// Load resolves the configuration from the process environment.
func Load() (Config, error) {
	return load(os.LookupEnv)
}

// load is the testable core: lookup mimics os.LookupEnv.
func load(lookup func(string) (string, bool)) (Config, error) {
	c := Config{
		Port:     DefaultPort,
		Host:     DefaultHost,
		DataDir:  DefaultDataDir,
		AdminKey: DefaultAdminKey,
		NodePath: DefaultNodePath,
	}

	strs := []struct {
		key string
		dst *string
	}{
		{EnvHost, &c.Host},
		{EnvDataDir, &c.DataDir},
		{EnvAdminKey, &c.AdminKey},
		{EnvNodePath, &c.NodePath},
	}
	for _, f := range strs {
		if v, ok := lookup(f.key); ok && v != "" {
			*f.dst = v
		}
	}

	if v, ok := lookup(EnvPort); ok && v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			// Only the port is echoed in errors; it is never a secret.
			return Config{}, fmt.Errorf("config: invalid %s=%q: want an integer port in 1..65535", EnvPort, v)
		}
		c.Port = p
	}
	return c, nil
}

// AccountsDB returns the path of the SQLite accounts database inside DataDir.
// The file name matches the legacy production layout (data/accounts.db).
func (c Config) AccountsDB() string {
	return filepath.Join(c.DataDir, "accounts.db")
}
