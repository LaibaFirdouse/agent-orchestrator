package report

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

var errSemanticUnsupported = errors.New("session: semantic message acceptance unsupported")

// Regression for #6119: a permanently failing older batch must not starve a
// later due batch in the same project within the same RunDue cycle.
func TestCoordinatorFailureDoesNotStarveNewerReportBatch(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	store := newIsolationStore(now)
	store.sessions = []domain.SessionRecord{{
		ID: "orch", ProjectID: "p", Kind: domain.KindOrchestrator, Mode: domain.SessionModeChat,
	}}

	old := reportAt("old", "worker-a", now.Add(-2*time.Minute), now.Add(-2*time.Minute), "old undeliverable")
	old.DeliveryBatchID = "report-batch:old"
	newer := reportAt("newer", "worker-b", now.Add(-time.Minute), now.Add(-time.Minute), "newer done")
	newer.DeliveryBatchID = "report-batch:newer"
	store.add(old, newer)

	delivery := &isolationDelivery{
		failKeys: map[string]error{"report-batch:old": errSemanticUnsupported},
	}
	c := NewCoordinator(CoordinatorDeps{
		Store: store, Delivery: delivery,
		Now:        func() time.Time { return now },
		NewToken:   func() string { return "claim-token" },
		RetryDelay: 5 * time.Second,
	})

	delay, err := c.RunDue(context.Background())
	if err != nil {
		t.Fatalf("RunDue err = %v, want nil after newer batch succeeds", err)
	}
	if delay != 5*time.Second {
		t.Fatalf("RunDue delay = %v, want backoff until deferred older batch", delay)
	}

	oldAttempts := store.attempts("old")
	newerAttempts := store.attempts("newer")
	if oldAttempts != 1 {
		t.Fatalf("old batch attempts = %d, want 1", oldAttempts)
	}
	if newerAttempts != 1 {
		t.Fatalf("newer batch attempts = %d, want 1; submitKeys=%v", newerAttempts, delivery.keys)
	}
	if store.acked != 1 {
		t.Fatalf("acked = %d, want newer batch acknowledged", store.acked)
	}
	if got := store.lastError("old"); got != errSemanticUnsupported.Error() {
		t.Fatalf("old last_error = %q", got)
	}
	if !store.availableAt("old").Equal(now.Add(5 * time.Second)) {
		t.Fatalf("old available_at = %s, want deferred by retryDelay", store.availableAt("old"))
	}
}

func TestCoordinatorFailureDoesNotStarveOtherProjectBatch(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	store := newIsolationStore(now)
	store.sessions = []domain.SessionRecord{
		{ID: "orch-a", ProjectID: "pa", Kind: domain.KindOrchestrator, Mode: domain.SessionModeChat},
		{ID: "orch-b", ProjectID: "pb", Kind: domain.KindOrchestrator, Mode: domain.SessionModeChat},
	}
	old := reportAt("old-a", "wa", now.Add(-2*time.Minute), now.Add(-2*time.Minute), "fail a")
	old.ProjectID = "pa"
	old.DeliveryBatchID = "report-batch:old-a"
	other := reportAt("other-b", "wb", now.Add(-time.Minute), now.Add(-time.Minute), "ok b")
	other.ProjectID = "pb"
	other.DeliveryBatchID = "report-batch:other-b"
	store.add(old, other)

	delivery := &isolationDelivery{failKeys: map[string]error{"report-batch:old-a": errSemanticUnsupported}}
	c := NewCoordinator(CoordinatorDeps{
		Store: store, Delivery: delivery,
		Now:        func() time.Time { return now },
		NewToken:   func() string { return "tok" },
		RetryDelay: time.Second,
	})

	if _, err := c.RunDue(context.Background()); err != nil {
		t.Fatalf("RunDue err = %v", err)
	}
	if store.attempts("old-a") != 1 || store.attempts("other-b") != 1 {
		t.Fatalf("attempts old-a=%d other-b=%d keys=%v", store.attempts("old-a"), store.attempts("other-b"), delivery.keys)
	}
	if store.acked != 1 {
		t.Fatalf("acked = %d, want other project acknowledged", store.acked)
	}
}

type isolationStore struct {
	*coordinatorStore
}

func newIsolationStore(now time.Time) *isolationStore {
	return &isolationStore{coordinatorStore: newCoordinatorStore(now)}
}

func (s *isolationStore) attempts(id string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.reports {
		if rec.ID == id {
			return rec.DeliveryAttempts
		}
	}
	return 0
}

func (s *isolationStore) lastError(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.reports {
		if rec.ID == id {
			return rec.LastError
		}
	}
	return ""
}

func (s *isolationStore) availableAt(id string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.reports {
		if rec.ID == id {
			return rec.AvailableAt
		}
	}
	return time.Time{}
}

func (s *isolationStore) ClaimPendingReportBatch(_ context.Context, project domain.ProjectID, token string, at time.Time) ([]domain.ReportRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var scheduled []domain.ReportRecord
	for _, rec := range s.reports {
		if rec.ProjectID == project && rec.DeliveryState == domain.ReportPending {
			scheduled = append(scheduled, rec)
		}
	}
	sortReports(scheduled)
	if len(scheduled) == 0 {
		return nil, nil
	}
	batchID := scheduled[0].DeliveryBatchID
	if batchID == "" {
		batchID = "report-batch:" + scheduled[0].ID
		for i := range s.reports {
			if s.reports[i].ProjectID == project && s.reports[i].DeliveryState == domain.ReportPending && s.reports[i].DeliveryBatchID == "" {
				s.reports[i].DeliveryBatchID = batchID
			}
		}
	}
	var claimed []domain.ReportRecord
	for i := range s.reports {
		if s.reports[i].ProjectID != project || s.reports[i].DeliveryState != domain.ReportPending {
			continue
		}
		if s.reports[i].DeliveryBatchID != batchID {
			continue
		}
		s.reports[i].DeliveryState = domain.ReportClaimed
		s.reports[i].ClaimToken = token
		s.reports[i].ClaimedAt = at
		s.reports[i].DeliveryAttempts++
		s.reports[i].LastError = ""
		claimed = append(claimed, s.reports[i])
	}
	sortReports(claimed)
	return claimed, nil
}

func (s *isolationStore) ReleaseReportBatch(_ context.Context, project domain.ProjectID, token string, at time.Time, reason string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for i := range s.reports {
		if s.reports[i].ProjectID == project && s.reports[i].DeliveryState == domain.ReportClaimed && s.reports[i].ClaimToken == token {
			if at.Before(s.reports[i].AvailableAt) {
				s.reports[i].AvailableAt = at
			}
			s.reports[i].DeliveryState = domain.ReportPending
			s.reports[i].ClaimToken = ""
			s.reports[i].ClaimedAt = time.Time{}
			s.reports[i].LastError = reason
			count++
		}
	}
	return count, nil
}

func (s *isolationStore) DeferReportBatch(_ context.Context, project domain.ProjectID, token string, at time.Time, reason string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for i := range s.reports {
		if s.reports[i].ProjectID == project && s.reports[i].DeliveryState == domain.ReportClaimed && s.reports[i].ClaimToken == token {
			if s.reports[i].AvailableAt.Before(at) {
				s.reports[i].AvailableAt = at
			}
			s.reports[i].DeliveryState = domain.ReportPending
			s.reports[i].ClaimToken = ""
			s.reports[i].ClaimedAt = time.Time{}
			s.reports[i].LastError = reason
			count++
		}
	}
	return count, nil
}

type isolationDelivery struct {
	keys     []string
	failKeys map[string]error
}

func (d *isolationDelivery) Submit(_ context.Context, _ domain.SessionID, _ string, key string) error {
	d.keys = append(d.keys, key)
	if err, ok := d.failKeys[key]; ok {
		return err
	}
	return nil
}

func (d *isolationDelivery) Interrupt(context.Context, domain.SessionID) error { return nil }
