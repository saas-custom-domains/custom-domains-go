package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var (
	errNotFound  = errors.New("not found")
	errHostTaken = errors.New("host already connected")
)

type Brand struct {
	ID   int64
	Slug string
	Name string
}

type Link struct {
	Slug string
	URL  string
}

type Domain struct {
	ID           int64
	BrandID      int64
	Host         string
	Status       string // "pending" or "active"
	HTTPSReady   bool
	ProviderUUID string
}

// Store keeps brands, links and custom domains in SQLite. The queries are
// plain SQL; swap the driver for Postgres in a real app.
type Store struct{ db *sql.DB }

func openStore(dir string) (*Store, error) {
	dsn := "file:" + filepath.Join(dir, "app.db") +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One writer at a time is all SQLite does anyway.
	db.SetMaxOpenConns(1)
	s := &Store{db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, s.seed()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS brands (
			id INTEGER PRIMARY KEY,
			slug TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS links (
			id INTEGER PRIMARY KEY,
			brand_id INTEGER NOT NULL
				REFERENCES brands(id) ON DELETE CASCADE,
			slug TEXT NOT NULL,
			url TEXT NOT NULL,
			UNIQUE (brand_id, slug)
		);

		-- host is stored normalized (lowercase, no port, no
		-- trailing dot) and is unique: one domain, one brand.
		CREATE TABLE IF NOT EXISTS custom_domains (
			id INTEGER PRIMARY KEY,
			brand_id INTEGER NOT NULL
				REFERENCES brands(id) ON DELETE CASCADE,
			host TEXT NOT NULL UNIQUE,
			status TEXT NOT NULL DEFAULT 'pending'
				CHECK (status IN ('pending', 'active')),
			https_ready INTEGER NOT NULL DEFAULT 0,
			provider_uuid TEXT UNIQUE,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS reports (
			id INTEGER PRIMARY KEY,
			brand_id INTEGER NOT NULL
				REFERENCES brands(id) ON DELETE CASCADE,
			link TEXT NOT NULL,
			client_ip TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`)
	return err
}

func (s *Store) seed() error {
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO brands (id, slug, name) VALUES
			(1, 'acme', 'Acme Rockets'),
			(2, 'globex', 'Globex');
		INSERT OR IGNORE INTO links (brand_id, slug, url) VALUES
			(1, 'launch', 'https://example.com/acme/launch-day'),
			(1, 'docs', 'https://example.com/acme/docs'),
			(2, 'careers', 'https://example.com/globex/careers'),
			(2, 'pricing', 'https://example.com/globex/pricing');`)
	return err
}

// BrandByActiveHost is the lookup every request on a customer domain
// depends on. Keep the UNIQUE index on custom_domains.host.
func (s *Store) BrandByActiveHost(
	ctx context.Context, host string,
) (Brand, error) {
	var b Brand
	err := s.db.QueryRowContext(ctx, `
		SELECT b.id, b.slug, b.name FROM brands b
		JOIN custom_domains d ON d.brand_id = b.id
		WHERE d.host = ? AND d.status = 'active'`,
		host).Scan(&b.ID, &b.Slug, &b.Name)
	return b, notFound(err)
}

func (s *Store) IsActiveHost(ctx context.Context, host string) (bool, error) {
	_, err := s.BrandByActiveHost(ctx, host)
	if errors.Is(err, errNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) Brands(ctx context.Context) ([]Brand, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, slug, name FROM brands ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var brands []Brand
	for rows.Next() {
		var b Brand
		if err := rows.Scan(&b.ID, &b.Slug, &b.Name); err != nil {
			return nil, err
		}
		brands = append(brands, b)
	}
	return brands, rows.Err()
}

func (s *Store) Links(ctx context.Context, brandID int64) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT slug, url FROM links WHERE brand_id = ? ORDER BY id`, brandID)
	if err != nil {
		return nil, err
	}
	var links []Link
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.Slug, &l.URL); err != nil {
			return nil, err
		}
		links = append(links, l)
	}
	return links, rows.Err()
}

func (s *Store) LinkURL(ctx context.Context, brandID int64, slug string) (string, error) {
	var url string
	err := s.db.QueryRowContext(ctx,
		`SELECT url FROM links WHERE brand_id = ? AND slug = ?`, brandID, slug).Scan(&url)
	return url, notFound(err)
}

const domainColumns = `id, brand_id, host, status, https_ready, coalesce(provider_uuid, '')`

func scanDomain(row interface{ Scan(...any) error }) (Domain, error) {
	var d Domain
	err := row.Scan(&d.ID, &d.BrandID, &d.Host, &d.Status, &d.HTTPSReady, &d.ProviderUUID)
	return d, notFound(err)
}

func (s *Store) Domains(ctx context.Context) ([]Domain, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+domainColumns+` FROM custom_domains ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var domains []Domain
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		domains = append(domains, d)
	}
	return domains, rows.Err()
}

func (s *Store) Domain(ctx context.Context, id int64) (Domain, error) {
	return scanDomain(s.db.QueryRowContext(ctx,
		`SELECT `+domainColumns+` FROM custom_domains WHERE id = ?`, id))
}

func (s *Store) DomainByProviderUUID(ctx context.Context, uuid string) (Domain, error) {
	return scanDomain(s.db.QueryRowContext(ctx,
		`SELECT `+domainColumns+` FROM custom_domains WHERE provider_uuid = ?`, uuid))
}

func (s *Store) DomainByHost(ctx context.Context, host string) (Domain, error) {
	return scanDomain(s.db.QueryRowContext(ctx,
		`SELECT `+domainColumns+` FROM custom_domains WHERE host = ?`, host))
}

func (s *Store) InsertDomain(
	ctx context.Context, brandID int64, host string,
) (Domain, error) {
	d, err := scanDomain(s.db.QueryRowContext(ctx, `
		INSERT INTO custom_domains (brand_id, host) VALUES (?, ?)
		RETURNING `+domainColumns, brandID, host))
	// The unique index on host is what stops two brands from
	// connecting the same domain.
	se, ok := errors.AsType[*sqlite.Error](err)
	if ok && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		return d, errHostTaken
	}
	return d, err
}

func (s *Store) SetStatus(ctx context.Context, id int64, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE custom_domains SET status = ? WHERE id = ?`, status, id)
	return err
}

func (s *Store) SetHTTPSReady(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE custom_domains SET https_ready = 1 WHERE id = ?`, id)
	return err
}

func (s *Store) SetProviderUUID(ctx context.Context, id int64, uuid string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE custom_domains SET provider_uuid = ? WHERE id = ?`, uuid, id)
	return err
}

func (s *Store) DeleteDomain(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM custom_domains WHERE id = ?`, id)
	return err
}

func (s *Store) AddReport(ctx context.Context, brandID int64, link, clientIP string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO reports (brand_id, link, client_ip) VALUES (?, ?, ?)`, brandID, link, clientIP)
	return err
}

func (s *Store) ReportCount(ctx context.Context, brandID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM reports WHERE brand_id = ?`, brandID).Scan(&n)
	return n, err
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return errNotFound
	}
	return err
}
