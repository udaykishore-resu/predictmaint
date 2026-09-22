package cmms

import (
	"context"
	"fmt"
	"sync"
)

// Simulated is an in-process CMMS: it accepts work orders, assigns
// sequential numbers, and remembers them so the demo and tests can inspect
// what would have reached Maximo or SAP PM. It also renders both vendor
// payloads so the mapping is exercised on every call.
type Simulated struct {
	mu      sync.Mutex
	seq     int
	byKey   map[string]ExternalRef
	records map[string]SimulatedRecord
}

// SimulatedRecord is what the simulated CMMS stores per work order.
type SimulatedRecord struct {
	Ref       ExternalRef     `json:"ref"`
	Order     WorkOrder       `json:"order"`
	Maximo    MaximoWorkOrder `json:"maximo"`
	SAPPM     SAPPMOrder      `json:"sap_pm"`
	Cancelled bool            `json:"cancelled"`
	Reason    string          `json:"cancel_reason,omitempty"`
}

// NewSimulated creates an empty simulated CMMS.
func NewSimulated() *Simulated {
	return &Simulated{byKey: map[string]ExternalRef{}, records: map[string]SimulatedRecord{}}
}

// Name implements CMMS.
func (s *Simulated) Name() string { return "simulated" }

// CreateWorkOrder implements CMMS idempotently on IdempotencyKey.
func (s *Simulated) CreateWorkOrder(ctx context.Context, wo WorkOrder) (ExternalRef, error) {
	if err := ctx.Err(); err != nil {
		return ExternalRef{}, err
	}
	if wo.IdempotencyKey == "" {
		return ExternalRef{}, fmt.Errorf("cmms: work order without idempotency key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ref, ok := s.byKey[wo.IdempotencyKey]; ok {
		return ref, nil
	}
	s.seq++
	ref := ExternalRef{System: s.Name(), ID: fmt.Sprintf("WO-%06d", s.seq)}
	s.byKey[wo.IdempotencyKey] = ref
	s.records[ref.ID] = SimulatedRecord{Ref: ref, Order: wo, Maximo: ToMaximo(wo), SAPPM: ToSAPPM(wo)}
	return ref, nil
}

// CancelWorkOrder implements CMMS.
func (s *Simulated) CancelWorkOrder(ctx context.Context, ref ExternalRef, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[ref.ID]
	if !ok {
		return fmt.Errorf("cmms: unknown work order %s", ref.ID)
	}
	rec.Cancelled = true
	rec.Reason = reason
	s.records[ref.ID] = rec
	return nil
}

// Records returns a snapshot of everything the simulated CMMS received.
func (s *Simulated) Records() []SimulatedRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SimulatedRecord, 0, len(s.records))
	for i := 1; i <= s.seq; i++ {
		if rec, ok := s.records[fmt.Sprintf("WO-%06d", i)]; ok {
			out = append(out, rec)
		}
	}
	return out
}
