package sqlite

import (
	"errors"
	"testing"
	"time"

	"github.com/zhanglei10281852-gif/ai/internal/domain"
	"github.com/zhanglei10281852-gif/ai/internal/repository"
)

func TestSnapshotFiltersIntersectWorkspaceAndZone(t *testing.T) {
	store, ctx, now := testStore(t)
	workspace, origin, destination, _, target := seedCatalog(t, store, ctx, now)

	otherWorkspace := workspace
	otherWorkspace.ID = "workspace_2"
	otherWorkspace.Code = "MESH-2"
	otherWorkspace.Name = "Other workspace"

	workspaceOnly := target
	workspaceOnly.ID = "snapshot_workspace_only"
	workspaceOnly.SourceZoneID = destination.ID
	workspaceOnly.SourceRevision = "REV-WORKSPACE-ONLY"

	zoneOnly := target
	zoneOnly.ID = "snapshot_zone_only"
	zoneOnly.WorkspaceID = otherWorkspace.ID
	zoneOnly.SourceRevision = "REV-ZONE-ONLY"

	if err := store.WithTx(ctx, func(tx repository.Tx) error {
		if err := tx.InsertWorkspace(ctx, otherWorkspace); err != nil {
			return err
		}
		if err := tx.InsertDatasetSnapshot(ctx, workspaceOnly); err != nil {
			return err
		}
		return tx.InsertDatasetSnapshot(ctx, zoneOnly)
	}); err != nil {
		t.Fatal(err)
	}

	var page repository.SnapshotPage
	if err := store.Read(ctx, func(reader repository.Reader) error {
		var err error
		page, err = reader.ListSnapshots(ctx, repository.SnapshotFilter{
			Page: repository.PageRequest{Limit: 10}, WorkspaceID: workspace.ID, DataZoneID: origin.ID,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != target.ID {
		t.Fatalf("combined snapshot filter page = %+v", page)
	}
}

func TestAuditListRejectsCorruptMetadata(t *testing.T) {
	store, ctx, now := testStore(t)
	_, err := store.db.ExecContext(ctx, `INSERT INTO audit_events(
		id, request_id, actor, action, entity_type, entity_id, outcome, metadata_json, created_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"audit_corrupt", "request-corrupt", "auditor-1", "snapshot_checked",
		"dataset_snapshot", "snapshot-1", "success", `{"source":"restore"`, formatTime(now))
	if err != nil {
		t.Fatal(err)
	}

	err = store.Read(ctx, func(reader repository.Reader) error {
		_, err := reader.ListAuditEvents(ctx, repository.AuditFilter{
			Page: repository.PageRequest{Limit: 10}, RequestID: "request-corrupt",
		})
		return err
	})
	if err == nil {
		t.Fatal("audit list accepted an event with unreadable metadata")
	}
}

func TestStaleCompletionCannotOverwriteDeadJob(t *testing.T) {
	store, ctx, now := testStore(t)
	job := domain.OutboxJob{
		ID: "job_terminal", Kind: "inference_run_planned", AggregateID: "run-terminal",
		Payload: []byte(`{"id":"run-terminal"}`), Status: domain.JobPending,
		MaxAttempts: 1, AvailableAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.WithTx(ctx, func(tx repository.Tx) error { return tx.InsertJob(ctx, job) }); err != nil {
		t.Fatal(err)
	}
	if err := store.WithTx(ctx, func(tx repository.Tx) error {
		claimed, err := tx.ClaimJobs(ctx, now, 1)
		if err != nil {
			return err
		}
		if len(claimed) != 1 || claimed[0].Status != domain.JobRunning {
			t.Fatalf("claimed jobs = %+v", claimed)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	releaseCompletion := make(chan struct{})
	completionWaiting := make(chan struct{})
	completionResult := make(chan error, 1)
	go func() {
		close(completionWaiting)
		<-releaseCompletion
		completionResult <- store.WithTx(ctx, func(tx repository.Tx) error {
			return tx.CompleteJob(ctx, job.ID, now.Add(2*time.Minute))
		})
	}()
	<-completionWaiting

	terminalErr := store.WithTx(ctx, func(tx repository.Tx) error {
		return tx.RetryJob(ctx, job.ID, now.Add(time.Minute), "attempts exhausted", true)
	})
	close(releaseCompletion)
	completionErr := <-completionResult
	if terminalErr != nil {
		t.Fatal(terminalErr)
	}
	if !errors.Is(completionErr, domain.ErrVersionConflict) {
		t.Fatalf("late completion error = %v", completionErr)
	}

	var summary repository.PlatformSummary
	if err := store.Read(ctx, func(reader repository.Reader) error {
		var err error
		summary, err = reader.GetPlatformSummary(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if summary.FailedJobs != 1 {
		t.Fatalf("failed jobs after late completion = %d, want 1", summary.FailedJobs)
	}
}

func TestInferenceRunWindowFiltersScheduledStartOnly(t *testing.T) {
	store, ctx, now := testStore(t)
	workspace, origin, destination, pool, _ := seedCatalog(t, store, ctx, now)
	longRun := domain.InferenceRun{
		ID: "run_long_window", WorkspaceID: workspace.ID, SourceZoneID: origin.ID,
		TargetZoneID: destination.ID, ComputePoolID: pool.ID, Reference: "RUN-LONG-WINDOW",
		State: domain.InferenceRunQueued, ScheduledStartAt: now.Add(2 * time.Hour),
		ExpectedFinishAt: now.Add(10 * time.Hour), TotalEstimatedRows: 100,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.WithTx(ctx, func(tx repository.Tx) error {
		return tx.InsertInferenceRun(ctx, longRun)
	}); err != nil {
		t.Fatal(err)
	}

	from := now.Add(time.Hour)
	to := now.Add(3 * time.Hour)
	var page repository.InferenceRunPage
	if err := store.Read(ctx, func(reader repository.Reader) error {
		var err error
		page, err = reader.ListInferenceRuns(ctx, repository.InferenceRunFilter{
			Page: repository.PageRequest{Limit: 10}, WorkspaceID: workspace.ID, From: &from, To: &to,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != longRun.ID {
		t.Fatalf("scheduled-start window page = %+v", page)
	}
}
