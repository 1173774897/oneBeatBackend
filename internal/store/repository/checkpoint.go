package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type ReconciliationCheckpoint struct {
	Environment       string
	JobName           string
	Direction         string
	WindowStart       *time.Time
	WindowEnd         *time.Time
	ContinuationToken string
	PageNumber        int
	Status            string
}

func (r *Repository) AcquireReconciliationLock(
	ctx context.Context,
	environment string,
	jobName string,
) (func(context.Context) error, bool, error) {
	connection, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var acquired bool
	lockKey := "onebeat:reconciliation:" + environment + ":" + jobName
	if err := connection.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, lockKey).Scan(&acquired); err != nil {
		connection.Release()
		return nil, false, err
	}
	if !acquired {
		connection.Release()
		return nil, false, nil
	}
	release := func(releaseContext context.Context) error {
		defer connection.Release()
		var unlocked bool
		if err := connection.QueryRow(
			releaseContext,
			`SELECT pg_advisory_unlock(hashtextextended($1, 0))`,
			lockKey,
		).Scan(&unlocked); err != nil {
			return err
		}
		if !unlocked {
			return errors.New("reconciliation advisory lock was not held")
		}
		return nil
	}
	return release, true, nil
}

func (r *Repository) LoadReconciliationCheckpoint(
	ctx context.Context,
	environment string,
	jobName string,
	direction string,
) (ReconciliationCheckpoint, error) {
	var checkpoint ReconciliationCheckpoint
	err := r.pool.QueryRow(ctx, `
		INSERT INTO iap_reconciliation_checkpoints
			(provider, environment, job_name, direction, status)
		VALUES ('HUAWEI', $1, $2, $3, 'IDLE')
		ON CONFLICT (provider, environment, job_name) DO UPDATE SET
			direction = iap_reconciliation_checkpoints.direction
		RETURNING environment, job_name, direction, window_start, window_end,
		          COALESCE(continuation_token, ''), page_number, status
	`, environment, jobName, direction).Scan(
		&checkpoint.Environment,
		&checkpoint.JobName,
		&checkpoint.Direction,
		&checkpoint.WindowStart,
		&checkpoint.WindowEnd,
		&checkpoint.ContinuationToken,
		&checkpoint.PageNumber,
		&checkpoint.Status,
	)
	if err != nil {
		return ReconciliationCheckpoint{}, fmt.Errorf("load reconciliation checkpoint: %w", err)
	}
	if checkpoint.Direction != direction {
		return ReconciliationCheckpoint{}, fmt.Errorf(
			"checkpoint %s direction is %s, expected %s",
			jobName,
			checkpoint.Direction,
			direction,
		)
	}
	return checkpoint, nil
}

func (r *Repository) BeginReconciliationWindow(
	ctx context.Context,
	environment string,
	jobName string,
	windowStart time.Time,
	windowEnd time.Time,
	continuationToken string,
	pageNumber int,
) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE iap_reconciliation_checkpoints SET
			window_start = $3,
			window_end = $4,
			continuation_token = NULLIF($5, ''),
			page_number = $6,
			status = 'RUNNING',
			last_error = NULL,
			updated_at = now()
		WHERE provider = 'HUAWEI' AND environment = $1 AND job_name = $2
	`, environment, jobName, windowStart, windowEnd, continuationToken, pageNumber)
	return err
}

func (r *Repository) SaveReconciliationPage(
	ctx context.Context,
	environment string,
	jobName string,
	nextToken string,
	pageNumber int,
) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE iap_reconciliation_checkpoints SET
			continuation_token = NULLIF($3, ''),
			page_number = $4,
			status = 'RUNNING',
			updated_at = now()
		WHERE provider = 'HUAWEI' AND environment = $1 AND job_name = $2
	`, environment, jobName, nextToken, pageNumber)
	return err
}

func (r *Repository) CompleteReconciliationWindow(
	ctx context.Context,
	environment string,
	jobName string,
	completedAt time.Time,
) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE iap_reconciliation_checkpoints SET
			continuation_token = NULL,
			page_number = 0,
			status = 'COMPLETED',
			last_success_at = $3,
			last_error = NULL,
			updated_at = $3
		WHERE provider = 'HUAWEI' AND environment = $1 AND job_name = $2
	`, environment, jobName, completedAt)
	return err
}

func (r *Repository) FailReconciliationWindow(
	ctx context.Context,
	environment string,
	jobName string,
	lastError string,
) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE iap_reconciliation_checkpoints SET
			status = 'FAILED', last_error = $3, updated_at = now()
		WHERE provider = 'HUAWEI' AND environment = $1 AND job_name = $2
	`, environment, jobName, lastError)
	return err
}

func (r *Repository) ResetReconciliationWindow(
	ctx context.Context,
	environment string,
	jobName string,
) error {
	commandTag, err := r.pool.Exec(ctx, `
		UPDATE iap_reconciliation_checkpoints SET
			continuation_token = NULL,
			page_number = 0,
			status = 'RESET_REQUIRED',
			updated_at = now()
		WHERE provider = 'HUAWEI' AND environment = $1 AND job_name = $2
	`, environment, jobName)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
