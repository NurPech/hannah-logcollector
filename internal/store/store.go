package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Source is one shipping component instance (from ShipHello).
type Source struct {
	ID        int64
	Component string
	Instance  string
	Version   string
}

// Entry is a single log line. Level and Category hold the raw proto enum values —
// the store doesn't interpret them.
type Entry struct {
	SourceID    int64
	TimestampMs int64
	Level       int32
	Logger      string
	Message     string
	Category    int32
}

// Gap records entries a component had to drop before they reached the collector.
type Gap struct {
	SourceID int64
	Dropped  int64
	FromMs   int64
	ToMs     int64
}

// SourceStats is a Source plus what the collector currently holds for it.
type SourceStats struct {
	Source
	OldestMs int64
	NewestMs int64
	Entries  int64
}

// Filter selects entries/gaps for an export. Zero values mean "no restriction".
type Filter struct {
	SinceMs           int64
	UntilMs           int64
	Components        []string
	ExcludeCategories []int32
}

// PruneResult reports how many entries a Prune call removed, and why.
type PruneResult struct {
	ByAge  int64
	BySize int64
}

type Store struct {
	db *sql.DB
}

func New(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	db.SetMaxOpenConns(1) // SQLite supports only one writer at a time

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func migrate(db *sql.DB) error {
	// Must run before the first table exists — only then does SQLite switch modes.
	// Lets Prune hand freed pages back to the filesystem via incremental_vacuum.
	if _, err := db.Exec(`PRAGMA auto_vacuum = INCREMENTAL`); err != nil {
		return err
	}
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS sources (
			id        INTEGER PRIMARY KEY,
			component TEXT    NOT NULL,
			instance  TEXT    NOT NULL,
			version   TEXT    NOT NULL DEFAULT '',
			UNIQUE (component, instance)
		);
		CREATE TABLE IF NOT EXISTS entries (
			id        INTEGER PRIMARY KEY,
			source_id INTEGER NOT NULL,
			ts_ms     INTEGER NOT NULL,
			level     INTEGER NOT NULL,
			logger    TEXT    NOT NULL,
			message   TEXT    NOT NULL,
			category  INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS entries_ts        ON entries (ts_ms);
		CREATE INDEX IF NOT EXISTS entries_source_ts ON entries (source_id, ts_ms);
		CREATE TABLE IF NOT EXISTS gaps (
			id        INTEGER PRIMARY KEY,
			source_id INTEGER NOT NULL,
			dropped   INTEGER NOT NULL,
			from_ms   INTEGER NOT NULL,
			to_ms     INTEGER NOT NULL
		);
	`)
	return err
}

// UpsertSource returns the ID for (component, instance), creating it if needed and
// updating its version to the one just announced.
func (s *Store) UpsertSource(ctx context.Context, component, instance, version string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO sources (component, instance, version) VALUES (?, ?, ?)
		ON CONFLICT (component, instance) DO UPDATE SET version = excluded.version
		RETURNING id
	`, component, instance, version).Scan(&id)
	return id, err
}

// AllSources returns every source ever seen, keyed by ID.
func (s *Store) AllSources(ctx context.Context) (map[int64]Source, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, component, instance, version FROM sources`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64]Source{}
	for rows.Next() {
		var src Source
		if err := rows.Scan(&src.ID, &src.Component, &src.Instance, &src.Version); err != nil {
			return nil, err
		}
		out[src.ID] = src
	}
	return out, rows.Err()
}

// InsertEntries stores a batch of entries in a single transaction.
func (s *Store) InsertEntries(ctx context.Context, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO entries (source_id, ts_ms, level, logger, message, category) VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, e := range entries {
		if _, err := stmt.ExecContext(ctx, e.SourceID, e.TimestampMs, e.Level, e.Logger, e.Message, e.Category); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) InsertGap(ctx context.Context, g Gap) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO gaps (source_id, dropped, from_ms, to_ms) VALUES (?, ?, ?, ?)`,
		g.SourceID, g.Dropped, g.FromMs, g.ToMs)
	return err
}

// Sources returns every source that currently has entries, with its time range.
func (s *Store) Sources(ctx context.Context) ([]SourceStats, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id, s.component, s.instance, s.version, MIN(e.ts_ms), MAX(e.ts_ms), COUNT(*)
		FROM entries e JOIN sources s ON s.id = e.source_id
		GROUP BY s.id
		ORDER BY s.component, s.instance
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SourceStats
	for rows.Next() {
		var st SourceStats
		if err := rows.Scan(&st.ID, &st.Component, &st.Instance, &st.Version, &st.OldestMs, &st.NewestMs, &st.Entries); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// EachEntry calls fn for every entry matching f, ordered by source, then time.
func (s *Store) EachEntry(ctx context.Context, f Filter, fn func(Entry) error) error {
	where, args := f.where("e")
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.source_id, e.ts_ms, e.level, e.logger, e.message, e.category
		FROM entries e JOIN sources s ON s.id = e.source_id
		`+where+`
		ORDER BY e.source_id, e.ts_ms, e.id
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.SourceID, &e.TimestampMs, &e.Level, &e.Logger, &e.Message, &e.Category); err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Gaps returns the gaps overlapping f's time range for f's components.
func (s *Store) Gaps(ctx context.Context, f Filter) ([]Gap, error) {
	var conds []string
	var args []any
	if f.SinceMs > 0 {
		conds = append(conds, "g.to_ms >= ?")
		args = append(args, f.SinceMs)
	}
	if f.UntilMs > 0 {
		conds = append(conds, "g.from_ms <= ?")
		args = append(args, f.UntilMs)
	}
	if len(f.Components) > 0 {
		conds = append(conds, "s.component IN ("+placeholders(len(f.Components))+")")
		for _, c := range f.Components {
			args = append(args, c)
		}
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT g.source_id, g.dropped, g.from_ms, g.to_ms
		FROM gaps g JOIN sources s ON s.id = g.source_id
		`+where+`
		ORDER BY g.source_id, g.from_ms
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Gap
	for rows.Next() {
		var g Gap
		if err := rows.Scan(&g.SourceID, &g.Dropped, &g.FromMs, &g.ToMs); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// UsedBytes is the space the data actually occupies (file size minus free pages).
func (s *Store) UsedBytes(ctx context.Context) (int64, error) {
	var pageCount, freelist, pageSize int64
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err != nil {
		return 0, err
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freelist); err != nil {
		return 0, err
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return 0, err
	}
	return (pageCount - freelist) * pageSize, nil
}

// pruneBatch is how many of the oldest entries Prune removes per round while over the size limit.
const pruneBatch = 1000

// Prune removes everything older than maxAge, then the oldest entries until the data
// fits into maxBytes — whichever limit is hit first wins.
func (s *Store) Prune(ctx context.Context, now time.Time, maxAge time.Duration, maxBytes int64) (PruneResult, error) {
	var res PruneResult
	cutoff := now.Add(-maxAge).UnixMilli()

	r, err := s.db.ExecContext(ctx, `DELETE FROM entries WHERE ts_ms < ?`, cutoff)
	if err != nil {
		return res, err
	}
	res.ByAge, _ = r.RowsAffected()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM gaps WHERE to_ms < ?`, cutoff); err != nil {
		return res, err
	}

	for {
		used, err := s.UsedBytes(ctx)
		if err != nil {
			return res, err
		}
		if used <= maxBytes {
			break
		}
		r, err := s.db.ExecContext(ctx, `
			DELETE FROM entries WHERE id IN (SELECT id FROM entries ORDER BY ts_ms, id LIMIT ?)
		`, pruneBatch)
		if err != nil {
			return res, err
		}
		n, _ := r.RowsAffected()
		if n == 0 {
			break // nothing left to delete — the remaining size is schema/index overhead
		}
		res.BySize += n
	}

	if res.ByAge > 0 || res.BySize > 0 {
		if _, err := s.db.ExecContext(ctx, `PRAGMA incremental_vacuum`); err != nil {
			return res, err
		}
	}
	return res, nil
}

func (f Filter) where(alias string) (string, []any) {
	var conds []string
	var args []any
	if f.SinceMs > 0 {
		conds = append(conds, alias+".ts_ms >= ?")
		args = append(args, f.SinceMs)
	}
	if f.UntilMs > 0 {
		conds = append(conds, alias+".ts_ms <= ?")
		args = append(args, f.UntilMs)
	}
	if len(f.Components) > 0 {
		conds = append(conds, "s.component IN ("+placeholders(len(f.Components))+")")
		for _, c := range f.Components {
			args = append(args, c)
		}
	}
	if len(f.ExcludeCategories) > 0 {
		conds = append(conds, alias+".category NOT IN ("+placeholders(len(f.ExcludeCategories))+")")
		for _, c := range f.ExcludeCategories {
			args = append(args, c)
		}
	}
	if len(conds) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
