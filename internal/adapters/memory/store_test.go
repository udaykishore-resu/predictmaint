package memory

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/predictmaint/internal/domain/alerting"
	"github.com/udaykishore-resu/predictmaint/internal/domain/cmms"
	"github.com/udaykishore-resu/predictmaint/internal/domain/template"
	"github.com/udaykishore-resu/predictmaint/internal/ports"
)

func TestStore(t *testing.T) {
	s := New()
	ctx := context.Background()
	require.NoError(t, s.Ping(ctx))

	// Alerts
	now := time.Now()
	a1 := alerting.Alert{ID: "a1", AssetID: "p1", Site: "s1", Status: alerting.StatusOpen, Contributors: []alerting.Contributor{{Feature: "f"}}, ClearedAt: &now, FeedbackAt: &now, Adaptation: &alerting.AdaptationResult{}}
	a2 := alerting.Alert{ID: "a2", AssetID: "p2", Site: "s2", Status: alerting.StatusConfirmed}
	require.NoError(t, s.SaveAlert(ctx, a1))
	require.NoError(t, s.SaveAlert(ctx, a2))
	got, err := s.GetAlert(ctx, "a1")
	require.NoError(t, err)
	got.Contributors[0].Feature = "mutated"
	again, _ := s.GetAlert(ctx, "a1")
	assert.Equal(t, "f", again.Contributors[0].Feature, "returned alerts are copies")
	_, err = s.GetAlert(ctx, "zz")
	assert.ErrorIs(t, err, ports.ErrNotFound)
	all, _ := s.ListAlerts(ctx, ports.AlertFilter{})
	require.Len(t, all, 2)
	assert.Equal(t, "a2", all[0].ID, "newest first")
	bySite, _ := s.ListAlerts(ctx, ports.AlertFilter{Site: "s1"})
	assert.Len(t, bySite, 1)
	byAsset, _ := s.ListAlerts(ctx, ports.AlertFilter{AssetID: "p2", Status: alerting.StatusConfirmed})
	assert.Len(t, byAsset, 1)
	limited, _ := s.ListAlerts(ctx, ports.AlertFilter{Limit: 1})
	assert.Len(t, limited, 1)
	a1.Status = alerting.StatusDismissed
	require.NoError(t, s.SaveAlert(ctx, a1))
	all, _ = s.ListAlerts(ctx, ports.AlertFilter{})
	assert.Len(t, all, 2, "upsert does not duplicate")

	// Work orders
	w1 := cmms.WorkOrder{ID: "w1", AssetID: "p1", Site: "s1", FailureMode: "bearing_wear", Status: cmms.StatusOpen}
	w2 := cmms.WorkOrder{ID: "w2", AssetID: "p1", Site: "s1", FailureMode: "bearing_wear", Status: cmms.StatusClosed}
	require.NoError(t, s.SaveWorkOrder(ctx, w1))
	require.NoError(t, s.SaveWorkOrder(ctx, w2))
	wo, err := s.GetWorkOrder(ctx, "w1")
	require.NoError(t, err)
	assert.Equal(t, "p1", wo.AssetID)
	_, err = s.GetWorkOrder(ctx, "nope")
	assert.ErrorIs(t, err, ports.ErrNotFound)
	open, err := s.FindOpenWorkOrder(ctx, "p1", "bearing_wear")
	require.NoError(t, err)
	assert.Equal(t, "w1", open.ID)
	_, err = s.FindOpenWorkOrder(ctx, "p1", "other")
	assert.ErrorIs(t, err, ports.ErrNotFound)
	wos, _ := s.ListWorkOrders(ctx, ports.WorkOrderFilter{Site: "s1", AssetID: "p1"})
	assert.Len(t, wos, 2)
	assert.Equal(t, "w2", wos[0].ID)
	wos, _ = s.ListWorkOrders(ctx, ports.WorkOrderFilter{Status: cmms.StatusOpen, Limit: 5})
	assert.Len(t, wos, 1)
	wos, _ = s.ListWorkOrders(ctx, ports.WorkOrderFilter{Limit: 1})
	assert.Len(t, wos, 1)

	// Templates
	require.NoError(t, s.PutTemplate(ctx, template.Template{Class: "pump"}))
	require.NoError(t, s.PutTemplate(ctx, template.Template{Class: "motor"}))
	tp, err := s.GetTemplate(ctx, "pump")
	require.NoError(t, err)
	assert.Equal(t, "pump", tp.Class)
	_, err = s.GetTemplate(ctx, "x")
	assert.ErrorIs(t, err, ports.ErrNotFound)
	tps, _ := s.ListTemplates(ctx)
	require.Len(t, tps, 2)
	assert.Equal(t, "motor", tps[0].Class)

	// Snapshots
	require.NoError(t, s.SaveAssetSnapshot(ctx, ports.AssetSnapshot{AssetID: "b"}))
	require.NoError(t, s.SaveAssetSnapshot(ctx, ports.AssetSnapshot{AssetID: "a"}))
	snaps, _ := s.ListAssetSnapshots(ctx)
	require.Len(t, snaps, 2)
	assert.Equal(t, "a", snaps[0].AssetID)

	require.NoError(t, s.Close())

	// Cancelled contexts propagate.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	assert.Error(t, s.SaveAlert(cctx, a1))
	_, err = s.GetAlert(cctx, "a1")
	assert.Error(t, err)
	_, err = s.ListAlerts(cctx, ports.AlertFilter{})
	assert.Error(t, err)
	assert.Error(t, s.SaveWorkOrder(cctx, w1))
	_, err = s.GetWorkOrder(cctx, "w1")
	assert.Error(t, err)
	_, err = s.ListWorkOrders(cctx, ports.WorkOrderFilter{})
	assert.Error(t, err)
	_, err = s.FindOpenWorkOrder(cctx, "p1", "bearing_wear")
	assert.Error(t, err)
	assert.Error(t, s.PutTemplate(cctx, template.Template{}))
	_, err = s.GetTemplate(cctx, "pump")
	assert.Error(t, err)
	_, err = s.ListTemplates(cctx)
	assert.Error(t, err)
	assert.Error(t, s.SaveAssetSnapshot(cctx, ports.AssetSnapshot{}))
	_, err = s.ListAssetSnapshots(cctx)
	assert.Error(t, err)
	assert.Error(t, s.Ping(cctx))
}
