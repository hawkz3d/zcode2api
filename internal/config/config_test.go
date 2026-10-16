package config

import (
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	c, err := load(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Port != DefaultPort || c.Host != DefaultHost || c.DataDir != DefaultDataDir ||
		c.AdminKey != DefaultAdminKey || c.NodePath != DefaultNodePath {
		t.Fatalf("defaults wrong: %+v", c)
	}
}

func TestLoadOverrides(t *testing.T) {
	env := map[string]string{
		EnvPort:     "9120",
		EnvHost:     "127.0.0.1",
		EnvDataDir:  "/var/lib/zcode",
		EnvAdminKey: "s3cret-do-not-log",
		EnvNodePath: "/usr/bin/node20",
	}
	c, err := load(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Port != 9120 {
		t.Errorf("port = %d, want 9120", c.Port)
	}
	if c.Host != "127.0.0.1" || c.DataDir != "/var/lib/zcode" || c.NodePath != "/usr/bin/node20" {
		t.Errorf("string overrides wrong: %+v", c)
	}
	if c.AdminKey != "s3cret-do-not-log" {
		t.Errorf("admin key not overridden")
	}
}

func TestEmptyIsTreatedAsUnset(t *testing.T) {
	// A variable present but empty must fall back to the default (documented
	// compatibility choice of the rewrite).
	env := map[string]string{EnvPort: "", EnvHost: ""}
	c, err := load(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Port != DefaultPort || c.Host != DefaultHost {
		t.Errorf("empty value should yield defaults: %+v", c)
	}
}

func TestInvalidPort(t *testing.T) {
	for _, bad := range []string{"abc", "0", "-1", "70000", "9999999999999999999"} {
		env := map[string]string{EnvPort: bad}
		_, err := load(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
		if err == nil {
			t.Errorf("port %q: want error, got nil", bad)
		}
	}
}

func TestAccountDBPath(t *testing.T) {
	c := Config{DataDir: "data"}
	if got := c.AccountsDB(); got != "data/accounts.db" {
		t.Errorf("AccountsDB = %q, want data/accounts.db", got)
	}
}

// Sanity: the exported Load() wires to the real process env without panicking
// and honours t.Setenv.
func TestLoadFromProcessEnv(t *testing.T) {
	t.Setenv(EnvPort, "9120")
	t.Setenv(EnvHost, "0.0.0.0")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Port != 9120 {
		t.Errorf("port = %d, want 9120", c.Port)
	}
}
