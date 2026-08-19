package worker

import (
	"context"
	"testing"
	"time"

	"github.com/zhanglei10281852-gif/ai/internal/domain"
	"github.com/zhanglei10281852-gif/ai/internal/repository"
)

func TestTransientJobBackoffUsesCurrentAttempt(t *testing.T) {
	worker, store, ctx, now := workerFixture(t)
	job := domain.OutboxJob{
		ID: "job_transient_backoff", Kind: "temporarily_unsupported", AggregateID: "run-backoff",
		Payload: []byte(`"run-backoff"`), Status: domain.JobPending,
		MaxAttempts: 5, AvailableAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.WithTx(ctx, func(tx repository.Tx) error { return tx.InsertJob(ctx, job) }); err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	var reclaimed []domain.OutboxJob
	if err := store.WithTx(ctx, func(tx repository.Tx) error {
		var err error
		reclaimed, err = tx.ClaimJobs(ctx, now.Add(2*time.Second), 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != job.ID || reclaimed[0].Attempts != 2 {
		t.Fatalf("jobs available after first retry interval = %+v", reclaimed)
	}
}

func TestPoisonJobDoesNotStopLaterJobsInBatch(t *testing.T) {
	tx := &annotationJobTx{
		jobs: []domain.OutboxJob{
			{
				ID: "job_invalid", Kind: "unsupported_event", AggregateID: "run-invalid",
				Status: domain.JobPending, MaxAttempts: 5,
			},
			{
				ID: "job_valid", Kind: "inference_run_planned", AggregateID: "run-valid",
				Payload: []byte(`"run-valid"`), Status: domain.JobPending, MaxAttempts: 5,
			},
		},
		statuses: make(map[string]domain.JobStatus),
	}
	store := &annotationJobStore{tx: tx}
	now := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	worker := New(store, fixedAnnotationClock{now: now}, time.Second, 10, nil)
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tx.statuses["job_invalid"] != domain.JobFailed {
		t.Fatalf("invalid job status = %q, want %q", tx.statuses["job_invalid"], domain.JobFailed)
	}
	if tx.statuses["job_valid"] != domain.JobSucceeded {
		t.Fatalf("later valid job status = %q, want %q", tx.statuses["job_valid"], domain.JobSucceeded)
	}
}

type fixedAnnotationClock struct{ now time.Time }

func (c fixedAnnotationClock) Now() time.Time { return c.now }

type annotationJobStore struct{ tx *annotationJobTx }

func (s *annotationJobStore) WithTx(ctx context.Context, fn func(repository.Tx) error) error {
	return fn(s.tx)
}

func (s *annotationJobStore) Read(ctx context.Context, fn func(repository.Reader) error) error {
	return fn(s.tx)
}

func (s *annotationJobStore) Ping(context.Context) error { return nil }
func (s *annotationJobStore) Close() error               { return nil }

type annotationJobTx struct {
	repository.Tx
	jobs     []domain.OutboxJob
	statuses map[string]domain.JobStatus
}

func (tx *annotationJobTx) ExpireApprovalTasks(context.Context, time.Time, int) ([]domain.ApprovalTask, error) {
	return nil, nil
}

func (tx *annotationJobTx) ClaimJobs(context.Context, time.Time, int) ([]domain.OutboxJob, error) {
	claimed := make([]domain.OutboxJob, len(tx.jobs))
	for i, job := range tx.jobs {
		job.Status = domain.JobRunning
		job.Attempts++
		claimed[i] = job.Clone()
		tx.statuses[job.ID] = domain.JobRunning
	}
	return claimed, nil
}

func (tx *annotationJobTx) RetryJob(_ context.Context, id string, _ time.Time, _ string, dead bool) error {
	status := domain.JobFailed
	if dead {
		status = domain.JobDead
	}
	tx.statuses[id] = status
	return nil
}

func (tx *annotationJobTx) CompleteJob(_ context.Context, id string, _ time.Time) error {
	tx.statuses[id] = domain.JobSucceeded
	return nil
}
