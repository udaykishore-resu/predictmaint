package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The adapter needs a live PostgreSQL; integration coverage comes from
// `make run-full`. These tests pin the parts that are pure.

func TestBuildList(t *testing.T) {
	q, args := buildList("alerts", map[string]string{}, 0)
	assert.Equal(t, "SELECT payload FROM alerts ORDER BY seq DESC", q)
	assert.Empty(t, args)

	q, args = buildList("work_orders", map[string]string{"site": "s1", "status": "open"}, 50)
	assert.Equal(t, "SELECT payload FROM work_orders WHERE site = $1 AND status = $2 ORDER BY seq DESC LIMIT $3", q)
	assert.Equal(t, []any{"s1", "open", 50}, args)

	q, args = buildList("alerts", map[string]string{"asset_id": "p1", "bogus": "x"}, 0)
	assert.Equal(t, "SELECT payload FROM alerts WHERE asset_id = $1 ORDER BY seq DESC", q, "unknown filter keys are ignored")
	assert.Equal(t, []any{"p1"}, args)
}

func TestOpenRejectsBadDSN(t *testing.T) {
	_, err := Open(context.Background(), "://not a dsn")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse dsn")
}
