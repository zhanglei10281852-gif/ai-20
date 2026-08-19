package service

import (
	"testing"
	"time"

	"github.com/zhanglei10281852-gif/ai/internal/domain"
	"github.com/zhanglei10281852-gif/ai/internal/repository"
	"github.com/zhanglei10281852-gif/ai/internal/requestmeta"
)

func TestDailyLimitUsesSourceZoneCutoffNotWorkspaceCalendar(t *testing.T) {
	f := newServiceFixture(t)
	ctx := f.as(f.ml_engineer)
	source, err := f.services.Catalog.CreateDataZone(ctx, domain.DataZone{
		Code: "CUTOFF-SOURCE", Name: "Cutoff source", Timezone: "Asia/Shanghai", DailyLimit: 1, CutoffHour: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := f.services.Catalog.CreateComputePool(ctx, domain.ComputePool{
		SerialNumber: "CUTOFF-POOL", CapacityRows: 1000,
		AttestationDueAt: f.clock.Now().Add(72 * time.Hour), LastReconciledAt: f.clock.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.services.Catalog.RegisterSnapshot(ctx, domain.DatasetSnapshot{
		WorkspaceID: f.workspace.ID, SourceZoneID: source.ID, SourceRevision: "CUTOFF-REV",
		SchemaFamily: "ranking-v3", PartitionCount: 1, EstimatedRows: 100,
		ExpiresAt: f.clock.Now().Add(72 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = f.services.Catalog.ValidateSnapshot(ctx, snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}

	beforeCutoff := time.Date(2026, 8, 18, 21, 30, 0, 0, time.UTC)
	existing := domain.InferenceRun{
		ID: "run_before_cutoff", WorkspaceID: f.workspace.ID, SourceZoneID: source.ID,
		TargetZoneID: f.destination.ID, ComputePoolID: pool.ID, Reference: "CUTOFF-EXISTING",
		State: domain.InferenceRunQueued, ScheduledStartAt: beforeCutoff,
		ExpectedFinishAt: beforeCutoff.Add(time.Hour), TotalEstimatedRows: 10,
		Version: 1, CreatedAt: f.clock.Now(), UpdatedAt: f.clock.Now(),
	}
	if err := f.store.WithTx(ctx, func(tx repository.Tx) error {
		return tx.InsertInferenceRun(ctx, existing)
	}); err != nil {
		t.Fatal(err)
	}

	afterCutoff := time.Date(2026, 8, 18, 22, 30, 0, 0, time.UTC)
	created, err := f.services.Inference.PlanInferenceRun(ctx, PlanInferenceRunInput{
		WorkspaceID: f.workspace.ID, SourceZoneID: source.ID, TargetZoneID: f.destination.ID,
		ComputePoolID: pool.ID, Reference: "CUTOFF-NEXT-DAY", SnapshotIDs: []string{snapshot.ID},
		ScheduledStartAt: afterCutoff, ExpectedFinishAt: afterCutoff.Add(time.Hour),
		IdempotencyKey: "cutoff-next-day-key",
	})
	if err != nil {
		t.Fatalf("plan in the next source-zone business day: %v", err)
	}
	if created.SourceZoneID != source.ID {
		t.Fatalf("created run source zone = %q, want %q", created.SourceZoneID, source.ID)
	}
}

func TestAuditRequestFilterIsAppliedBeforePagination(t *testing.T) {
	f := newServiceFixture(t)
	ctx := f.as(f.ml_engineer)
	events := []domain.AuditEvent{
		{
			ID: "audit_requested", RequestID: "request-target", Actor: f.ml_engineer.UserID,
			Action: "run_inspected", EntityType: "inference_run", EntityID: "run-target",
			Outcome: "success", Metadata: map[string]string{"source": "api"},
			CreatedAt: f.clock.Now().Add(time.Minute),
		},
		{
			ID: "audit_newer", RequestID: "request-other", Actor: f.ml_engineer.UserID,
			Action: "run_inspected", EntityType: "inference_run", EntityID: "run-other",
			Outcome: "success", Metadata: map[string]string{"source": "worker"},
			CreatedAt: f.clock.Now().Add(2 * time.Minute),
		},
	}
	if err := f.store.WithTx(ctx, func(tx repository.Tx) error {
		for _, event := range events {
			if err := tx.InsertAuditEvent(ctx, event); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	page, err := f.services.Query.Audit(ctx, repository.AuditFilter{
		Page: repository.PageRequest{Limit: 1}, RequestID: "request-target",
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "audit_requested" {
		t.Fatalf("filtered audit page = %+v", page)
	}
}

func TestInferenceRunDetailLoadsOnlyItsOwnSnapshots(t *testing.T) {
	f := newServiceFixture(t)
	ctx := f.as(f.ml_engineer)
	run, err := f.services.Inference.PlanInferenceRun(ctx, PlanInferenceRunInput{
		WorkspaceID: f.workspace.ID, SourceZoneID: f.origin.ID, TargetZoneID: f.destination.ID,
		ComputePoolID: f.compute_pool.ID, Reference: "RUN-DETAIL", SnapshotIDs: []string{f.batch.ID},
		ScheduledStartAt: f.clock.Now().Add(time.Hour), ExpectedFinishAt: f.clock.Now().Add(2 * time.Hour),
		IdempotencyKey: "run-detail-key",
	})
	if err != nil {
		t.Fatal(err)
	}

	detail, items, err := f.services.Query.InferenceRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ID != run.ID {
		t.Fatalf("detail run ID = %q, want %q", detail.ID, run.ID)
	}
	if len(items) != 1 || items[0].ID != f.batch.ID || items[0].InferenceRunID != run.ID {
		t.Fatalf("detail snapshots = %+v", items)
	}
}

func TestAuditAttributesMutationToAuthenticatedPrincipal(t *testing.T) {
	f := newServiceFixture(t)
	requestID := "request-authenticated-actor"
	ctx := requestmeta.WithRequestID(f.as(f.ml_engineer), requestID)
	workspace, err := f.services.Catalog.CreateWorkspace(ctx, domain.Workspace{
		Code: "ACTOR-WORKSPACE", Name: "Actor attribution", Score: f.workspace.Score,
		MaxExecution: 8 * time.Hour, ReviewDeadline: 2 * time.Hour, BusinessTimezone: "Asia/Shanghai",
	})
	if err != nil {
		t.Fatal(err)
	}

	page, err := f.services.Query.Audit(ctx, repository.AuditFilter{
		Page: repository.PageRequest{Limit: 10}, RequestID: requestID,
		EntityType: "workspace", EntityID: workspace.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("workspace audit page = %+v", page)
	}
	if page.Items[0].Actor != f.ml_engineer.UserID {
		t.Fatalf("workspace audit actor = %q, want %q", page.Items[0].Actor, f.ml_engineer.UserID)
	}
}
