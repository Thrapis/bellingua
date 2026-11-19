package api

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/Thrapis/bellingua/internal/backup"
	"github.com/Thrapis/bellingua/internal/config"
)

// Settings edited in the UI. They are written into bellingua.yaml (comments
// and everything else kept, see config.Patch) and applied to the running
// server where that is possible. Secrets (tokens) are never read or written
// here: they stay in the file or the environment, and only "set / not set" is
// reported.

// SetConfigPath tells the server which file the settings page edits.
func (s *Server) SetConfigPath(path string) {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	s.cfgMu.Lock()
	s.cfgPath = path
	s.cfgMu.Unlock()
}

// config returns the settings now in force.
func (s *Server) config() config.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

type sftpSettings struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	Key        string `json:"key"`
	KnownHosts string `json:"knownHosts"`
	Dir        string `json:"dir"`
}

type dataSettings struct {
	URL      string `json:"url"`
	App      string `json:"app"`
	Folder   string `json:"folder"`
	CAFile   string `json:"caFile"`
	Timeout  string `json:"timeout"`
	TokenSet bool   `json:"tokenSet"` // output only: the token itself is never exposed
}

type backupSettings struct {
	Interval string        `json:"interval"`
	Keep     int           `json:"keep"`
	Dir      string        `json:"dir"`
	Projects []string      `json:"projects"` // empty = every project
	SFTP     *sftpSettings `json:"sftp"`
	Data     *dataSettings `json:"data"`
}

type settingsOut struct {
	Path    string `json:"path"` // the file edited; empty when there is none
	General struct {
		LogLevel string `json:"logLevel"`
		Listen   string `json:"listen"` // read-only here
		DB       string `json:"db"`     // read-only here
	} `json:"general"`
	Backup  backupSettings `json:"backup"`
	Crowdin struct {
		BaseURL  string `json:"baseUrl"`
		TokenSet bool   `json:"tokenSet"`
	} `json:"crowdin"`
	Projects        []string `json:"projects"`                  // every project name, for the backup choice
	RestartRequired []string `json:"restartRequired,omitempty"` // PUT only: settings that apply after a restart
}

func (s *Server) settingsView(ctx context.Context) (settingsOut, error) {
	c := s.config()
	s.cfgMu.RLock()
	path := s.cfgPath
	s.cfgMu.RUnlock()
	var o settingsOut
	o.Path = path
	o.General.LogLevel, o.General.Listen, o.General.DB = c.LogLevel, c.Listen, c.DB
	b := c.Backup
	o.Backup = backupSettings{Interval: b.Interval.String(), Keep: b.Keep, Dir: b.Dir, Projects: append([]string{}, b.Projects...)}
	if b.Interval == 0 {
		o.Backup.Interval = "0"
	}
	if x := b.SFTP; x != nil {
		o.Backup.SFTP = &sftpSettings{x.Host, x.Port, x.User, x.Key, x.KnownHosts, x.Dir}
	}
	if x := b.Data; x != nil {
		o.Backup.Data = &dataSettings{URL: x.URL, App: x.App, Folder: x.Folder, CAFile: x.CAFile, Timeout: x.Timeout.String(), TokenSet: x.Token != ""}
	}
	o.Crowdin.BaseURL, o.Crowdin.TokenSet = c.Crowdin.BaseURL, c.Crowdin.Token != ""
	ps, err := s.st.Projects(ctx)
	if err != nil {
		return o, err
	}
	o.Projects = make([]string, 0, len(ps))
	for _, p := range ps {
		o.Projects = append(o.Projects, p.Name)
	}
	return o, nil
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) error {
	o, err := s.settingsView(r.Context())
	if err != nil {
		return err
	}
	return ok(w, o)
}

// settingsIn is what the page sends: only the sections it wants to change.
// Pointers tell "not sent" from "empty" (which removes the key again).
type settingsIn struct {
	General *struct {
		LogLevel *string `json:"logLevel"`
	} `json:"general"`
	Backup *struct {
		Interval *string       `json:"interval"`
		Keep     *int          `json:"keep"`
		Dir      *string       `json:"dir"`
		Projects *[]string     `json:"projects"`
		SFTP     *sftpSettings `json:"sftp"` // an all-empty object turns the target off
		Data     *dataSettings `json:"data"`
	} `json:"backup"`
	Crowdin *struct {
		BaseURL *string `json:"baseUrl"`
	} `json:"crowdin"`
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) error {
	var in settingsIn
	if err := decode(r, &in); err != nil {
		return err
	}
	s.cfgMu.RLock()
	path := s.cfgPath
	s.cfgMu.RUnlock()
	if path == "" {
		return &httpError{code: http.StatusConflict, msg: "There is no config file to save into."}
	}
	edits := map[string]any{}
	set := func(key string, v any) { edits[key] = v }
	restart := []string{}

	if g := in.General; g != nil && g.LogLevel != nil {
		lvl := strings.ToLower(strings.TrimSpace(*g.LogLevel))
		switch lvl {
		case "", "debug", "info", "warn", "error":
		default:
			return badRequest("logLevel must be debug, info, warn or error")
		}
		set("logLevel", lvl)
		if lvl != s.config().LogLevel && !(lvl == "" && s.config().LogLevel == "info") {
			restart = append(restart, "logLevel")
		}
	}
	if b := in.Backup; b != nil {
		if s.backup.Status().Running {
			return &httpError{code: http.StatusConflict, msg: "A backup is running; save the backup settings when it has finished."}
		}
		if b.Interval != nil {
			iv := strings.TrimSpace(*b.Interval)
			if iv == "0" {
				iv = "0s"
			}
			if iv != "" {
				if _, err := time.ParseDuration(iv); err != nil {
					return badRequest("interval %q: use a duration like 15m or 2h (0 = only manual)", *b.Interval)
				}
			}
			set("backup.interval", iv)
		}
		if b.Keep != nil {
			if *b.Keep < 0 {
				return badRequest("keep must not be negative")
			}
			if *b.Keep == 0 {
				set("backup.keep", nil) // the default
			} else {
				set("backup.keep", *b.Keep)
			}
		}
		if b.Dir != nil {
			set("backup.dir", strings.TrimSpace(*b.Dir))
		}
		if b.Projects != nil {
			names := []string{}
			for _, n := range *b.Projects {
				if n = strings.TrimSpace(n); n != "" {
					names = append(names, n)
				}
			}
			set("backup.projects", names)
		}
		if x := b.SFTP; x != nil && *x == (sftpSettings{}) {
			set("backup.sftp", nil) // turned off: the whole section goes
		} else if x != nil {
			set("backup.sftp.host", strings.TrimSpace(x.Host))
			set("backup.sftp.port", portOrNil(x.Port))
			set("backup.sftp.user", strings.TrimSpace(x.User))
			set("backup.sftp.key", strings.TrimSpace(x.Key))
			set("backup.sftp.knownHosts", strings.TrimSpace(x.KnownHosts))
			set("backup.sftp.dir", strings.TrimSpace(x.Dir))
		}
		if x := b.Data; x != nil && (dataSettings{URL: x.URL, App: x.App, Folder: x.Folder, CAFile: x.CAFile, Timeout: x.Timeout}) == (dataSettings{}) {
			// Turned off: the whole section goes, a token written in it too.
			set("backup.data", nil)
		} else if x != nil { // the token is otherwise not touched
			set("backup.data.url", strings.TrimSpace(x.URL))
			set("backup.data.app", strings.TrimSpace(x.App))
			set("backup.data.folder", strings.TrimSpace(x.Folder))
			set("backup.data.caFile", strings.TrimSpace(x.CAFile))
			if t := strings.TrimSpace(x.Timeout); t == "" || t == "0s" {
				set("backup.data.timeout", nil)
			} else if _, err := time.ParseDuration(t); err != nil {
				return badRequest("data timeout %q: use a duration like 10m", x.Timeout)
			} else {
				set("backup.data.timeout", t)
			}
		}
	}
	if c := in.Crowdin; c != nil && c.BaseURL != nil {
		u := strings.TrimSpace(*c.BaseURL)
		if u != "" && !strings.HasPrefix(u, "https://") {
			return badRequest("the Crowdin API address must start with https://")
		}
		set("crowdin.baseUrl", u)
	}
	if len(edits) == 0 {
		return badRequest("nothing to change")
	}

	if err := config.Patch(path, edits); err != nil {
		return badRequest("%v", err)
	}
	nc, err := config.Load(path, false)
	if err != nil {
		return err
	}
	// Only what is safe to change under a running server is taken over.
	s.cfgMu.Lock()
	s.cfg.Backup = nc.Backup
	s.cfg.Crowdin = nc.Crowdin
	s.cfgMu.Unlock()
	if in.Backup != nil {
		if err := s.backup.Reconfigure(nc.Backup); err != nil {
			if errors.Is(err, backup.ErrRunning) {
				return &httpError{code: http.StatusConflict, msg: "The settings are saved, but a backup started meanwhile; restart bellingua to apply them."}
			}
			return err
		}
	}
	o, err := s.settingsView(r.Context())
	if err != nil {
		return err
	}
	o.RestartRequired = restart
	return ok(w, o)
}

func portOrNil(p int) any {
	if p <= 0 {
		return nil
	}
	return p
}
