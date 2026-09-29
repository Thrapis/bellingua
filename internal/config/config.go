// Package config loads bellingua.yaml.
package config

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the whole file. Every field has a working default, so the file
// itself is optional.
type Config struct {
	DB        string    `yaml:"db"`       // SQLite file
	Listen    string    `yaml:"listen"`   // HTTP address; keep it on localhost
	LogLevel  string    `yaml:"logLevel"` // debug | info | warn | error
	MT        MT        `yaml:"mt"`
	Lingvanex Lingvanex `yaml:"lingvanex"`
	Backup    Backup    `yaml:"backup"`
}

// Lingvanex configures the local translation server that bellingua starts
// and stops itself (third_party/lingvanex-server). Its address is the
// lingvanex provider's URL.
type Lingvanex struct {
	// Manage starts the server with "serve" (unless something already
	// listens on its port) and stops it on exit. Default true.
	Manage *bool `yaml:"manage"`
	// Workdir holds server.py and the model folders. A relative path is
	// looked up in the working directory, then next to the executable.
	Workdir       string        `yaml:"workdir"`
	Command       []string      `yaml:"command"`       // default: py ./server.py (Windows), python3 ./server.py
	HealthTimeout time.Duration `yaml:"healthTimeout"` // wait for the port; importing ctranslate2 is slow
	StopTimeout   time.Duration `yaml:"stopTimeout"`
}

// Managed reports whether bellingua should run the server.
func (l Lingvanex) Managed() bool { return l.Manage == nil || *l.Manage }

// MT configures machine translation providers, tried in order.
type MT struct {
	Providers   []Provider `yaml:"providers"`
	Concurrency int        `yaml:"concurrency"` // batch jobs; keep <= the Lingvanex server's INTER_THREADS
}

// Provider is one MT backend.
type Provider struct {
	Name    string        `yaml:"name"` // lingvanex
	URL     string        `yaml:"url"`  // lingvanex: server base URL
	Timeout time.Duration `yaml:"timeout"`
}

// Backup configures snapshots of the database. With neither Dir nor SFTP
// set, backups are disabled.
type Backup struct {
	Interval time.Duration `yaml:"interval"` // automatic snapshot period when there were edits; 0 = off
	Keep     int           `yaml:"keep"`     // snapshots to retain per target
	Dir      string        `yaml:"dir"`      // local (or mounted) directory target
	SFTP     *SFTP         `yaml:"sftp"`     // remote target over SSH
}

// SFTP is an SSH target (e.g. the VPS). Only key authentication is supported.
type SFTP struct {
	Host       string `yaml:"host"`
	Port       int    `yaml:"port"`
	User       string `yaml:"user"`
	Key        string `yaml:"key"`        // private key file, e.g. ~/.ssh/id_ed25519
	KnownHosts string `yaml:"knownHosts"` // default ~/.ssh/known_hosts
	Dir        string `yaml:"dir"`        // remote directory
}

// Default returns the configuration used when no file exists.
func Default() Config {
	return Config{
		DB:       "bellingua.db",
		Listen:   "127.0.0.1:7070",
		LogLevel: "info",
		MT: MT{
			Providers: []Provider{
				{Name: "lingvanex", URL: "http://127.0.0.1:8000", Timeout: 5 * time.Minute},
			},
			Concurrency: 2,
		},
		Lingvanex: Lingvanex{
			Workdir:       "third_party/lingvanex-server",
			Command:       defaultPython(),
			HealthTimeout: 90 * time.Second,
			StopTimeout:   10 * time.Second,
		},
		Backup: Backup{Interval: 15 * time.Minute, Keep: 20},
	}
}

func defaultPython() []string {
	if runtime.GOOS == "windows" {
		return []string{"py", "./server.py"}
	}
	return []string{"python3", "./server.py"}
}

// Load reads path over the defaults. A missing file is not an error when
// optional is set.
func Load(path string, optional bool) (Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && optional {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if c.MT.Concurrency <= 0 {
		c.MT.Concurrency = 1
	}
	if c.Lingvanex.HealthTimeout <= 0 {
		c.Lingvanex.HealthTimeout = 90 * time.Second
	}
	if c.Lingvanex.StopTimeout <= 0 {
		c.Lingvanex.StopTimeout = 10 * time.Second
	}
	if len(c.Lingvanex.Command) == 0 {
		c.Lingvanex.Command = defaultPython()
	}
	if c.Backup.Keep <= 0 {
		c.Backup.Keep = 20
	}
	if s := c.Backup.SFTP; s != nil {
		if s.Host == "" || s.User == "" || s.Key == "" || s.Dir == "" {
			return c, fmt.Errorf("%s: backup.sftp needs host, user, key and dir", path)
		}
		if s.Port == 0 {
			s.Port = 22
		}
	}
	return c, nil
}
