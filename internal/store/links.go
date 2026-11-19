package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// LinkCrowdin is the kind of a link to a Crowdin project.
const LinkCrowdin = "crowdin"

// Link ties a project to the remote project it is pulled from (migration 10).
// The access token is not part of it.
type Link struct {
	ProjectID  int64  `json:"projectId"`
	Kind       string `json:"kind"`
	RemoteID   int64  `json:"remoteId"`
	Identifier string `json:"identifier"` // lower case, as in Crowdin editor URLs
	BaseURL    string `json:"baseUrl,omitempty"`
	SourceLang string `json:"sourceLang"` // the remote's own language ids
	TargetLang string `json:"targetLang"`
	LastPull   int64  `json:"lastPull"` // unix seconds, 0 = never
}

const linkCols = "project_id, kind, remote_id, identifier, base_url, source_lang, target_lang, last_pull"

func scanLink(sc interface{ Scan(...any) error }) (Link, error) {
	var l Link
	err := sc.Scan(&l.ProjectID, &l.Kind, &l.RemoteID, &l.Identifier, &l.BaseURL, &l.SourceLang, &l.TargetLang, &l.LastPull)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

// Link returns a project's link of one kind, or ErrNotFound.
func (s *Store) Link(ctx context.Context, projectID int64, kind string) (Link, error) {
	return scanLink(s.R.QueryRowContext(ctx, "SELECT "+linkCols+" FROM project_links WHERE project_id = ? AND kind = ?", projectID, kind))
}

// LinkByIdentifier finds the project linked under an editor identifier.
func (s *Store) LinkByIdentifier(ctx context.Context, kind, identifier string) (Link, error) {
	return scanLink(s.R.QueryRowContext(ctx, "SELECT "+linkCols+" FROM project_links WHERE kind = ? AND identifier = ? AND identifier <> '' LIMIT 1",
		kind, strings.ToLower(identifier)))
}

// SetLink creates or replaces a link; when it was last pulled is kept.
func (s *Store) SetLink(ctx context.Context, l Link) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO project_links (project_id, kind, remote_id, identifier, base_url, source_lang, target_lang)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (project_id, kind) DO UPDATE SET remote_id = excluded.remote_id, identifier = excluded.identifier,
				base_url = excluded.base_url, source_lang = excluded.source_lang, target_lang = excluded.target_lang`,
			l.ProjectID, l.Kind, l.RemoteID, strings.ToLower(l.Identifier), l.BaseURL, l.SourceLang, l.TargetLang)
		return err
	})
}

// MarkPulled records the time of a finished pull.
func (s *Store) MarkPulled(ctx context.Context, projectID int64, kind string, at int64) error {
	if at == 0 {
		at = time.Now().Unix()
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE project_links SET last_pull = ? WHERE project_id = ? AND kind = ?", at, projectID, kind)
		return err
	})
}
