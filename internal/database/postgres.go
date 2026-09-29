package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultMaxConnections = 10

// Status contains non-sensitive information used by the database test endpoint.
type Status struct {
	Name             string
	User             string
	ServerTime       time.Time
	MigrationVersion int64
	MigrationDirty   bool
}

// Store owns the PostgreSQL connection pool used by the API.
type Store struct {
	pool *pgxpool.Pool
}

// Open creates a bounded connection pool and verifies that PostgreSQL is reachable.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("DATABASE_URL is required")
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	config.MaxConns = defaultMaxConnections
	config.MinConns = 1
	config.MaxConnIdleTime = 5 * time.Minute
	config.MaxConnLifetime = 30 * time.Minute
	config.HealthCheckPeriod = time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}

	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Status verifies both connectivity and that golang-migrate initialized the schema.
func (s *Store) Status(ctx context.Context) (Status, error) {
	var status Status
	err := s.pool.QueryRow(ctx, `
		SELECT
			current_database(),
			current_user,
			clock_timestamp(),
			version,
			dirty
		FROM schema_migrations
		LIMIT 1
	`).Scan(
		&status.Name,
		&status.User,
		&status.ServerTime,
		&status.MigrationVersion,
		&status.MigrationDirty,
	)
	if err != nil {
		return Status{}, fmt.Errorf("query database status: %w", err)
	}
	return status, nil
}
