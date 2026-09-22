// Package kafka consumes plantstream-shaped metrics from a Kafka topic and
// hands them to the engine in batches. Offsets are committed only after the
// engine has processed a batch (at-least-once); the engine's replay guard
// makes redelivery harmless.
package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/udaykishore-resu/predictmaint/internal/domain/metric"
	"github.com/udaykishore-resu/predictmaint/internal/ports"
)

// Config for the consumer.
type Config struct {
	Brokers  []string
	Topic    string
	GroupID  string
	ClientID string
	// MaxBatch caps records per handler call; PollTimeout bounds each poll.
	MaxBatch    int
	PollTimeout time.Duration
}

// BatchObserver is notified per processed batch ("ok" | "error").
type BatchObserver interface{ KafkaBatch(result string) }

type nopObserver struct{}

func (nopObserver) KafkaBatch(string) {}

// Consumer implements ports.MetricSource over franz-go.
type Consumer struct {
	cfg    Config
	log    *slog.Logger
	obs    BatchObserver
	client *kgo.Client
}

var _ ports.MetricSource = (*Consumer)(nil)

// New creates a consumer. It connects lazily on Run.
func New(cfg Config, log *slog.Logger, obs BatchObserver) (*Consumer, error) {
	if len(cfg.Brokers) == 0 {
		return nil, errors.New("kafka: at least one broker is required")
	}
	if cfg.Topic == "" || cfg.GroupID == "" {
		return nil, errors.New("kafka: topic and group id are required")
	}
	if cfg.MaxBatch <= 0 {
		cfg.MaxBatch = 1000
	}
	if cfg.PollTimeout <= 0 {
		cfg.PollTimeout = time.Second
	}
	if cfg.ClientID == "" {
		cfg.ClientID = "predictmaint"
	}
	if obs == nil {
		obs = nopObserver{}
	}
	if log == nil {
		log = slog.Default()
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ConsumerGroup(cfg.GroupID),
		kgo.ConsumeTopics(cfg.Topic),
		kgo.ClientID(cfg.ClientID),
		kgo.DisableAutoCommit(),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.BlockRebalanceOnPoll(),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka: new client: %w", err)
	}
	return &Consumer{cfg: cfg, log: log, obs: obs, client: client}, nil
}

// Ping checks broker connectivity (readiness).
func (c *Consumer) Ping(ctx context.Context) error {
	return c.client.Ping(ctx)
}

// Close releases the client.
func (c *Consumer) Close() { c.client.Close() }

// Run polls until ctx is cancelled. Batches that fail in the handler are not
// committed and will be redelivered after the next rebalance or restart.
func (c *Consumer) Run(ctx context.Context, h ports.MetricHandler) error {
	defer c.client.Close()
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		pollCtx, cancel := context.WithTimeout(ctx, c.cfg.PollTimeout)
		fetches := c.client.PollRecords(pollCtx, c.cfg.MaxBatch)
		cancel()
		if fetches.IsClientClosed() {
			return nil
		}
		if errs := fetches.Errors(); len(errs) > 0 {
			retriable := false
			for _, fe := range errs {
				if errors.Is(fe.Err, context.DeadlineExceeded) || errors.Is(fe.Err, context.Canceled) {
					retriable = true
					continue
				}
				c.log.Error("kafka fetch error", "topic", fe.Topic, "partition", fe.Partition, "err", fe.Err)
			}
			if !retriable {
				time.Sleep(500 * time.Millisecond)
			}
		}
		batch, skipped := Decode(fetches.Records())
		if skipped > 0 {
			c.log.Warn("skipped undecodable records", "count", skipped)
		}
		if len(batch) == 0 {
			c.client.AllowRebalance()
			continue
		}
		if err := h(ctx, batch); err != nil {
			c.obs.KafkaBatch("error")
			c.log.Error("batch handler failed; not committing", "records", len(batch), "err", err)
			c.client.AllowRebalance()
			time.Sleep(time.Second)
			continue
		}
		if err := c.client.CommitUncommittedOffsets(ctx); err != nil && ctx.Err() == nil {
			c.log.Error("offset commit failed", "err", err)
		}
		c.obs.KafkaBatch("ok")
		c.client.AllowRebalance()
	}
}

// Decode turns Kafka records into metrics. Each record value is a JSON
// metric or a JSON array of metrics. The record key, when set and the
// payload lacks an asset id, is used as the asset id (plantstream keys
// topics by asset). Undecodable records are counted and dropped: poison
// pills must never wedge a partition.
func Decode(records []*kgo.Record) ([]metric.Metric, int) {
	var out []metric.Metric
	skipped := 0
	for _, r := range records {
		if len(r.Value) == 0 {
			skipped++
			continue
		}
		var ms []metric.Metric
		if r.Value[0] == '[' {
			if err := json.Unmarshal(r.Value, &ms); err != nil {
				skipped++
				continue
			}
		} else {
			var m metric.Metric
			if err := json.Unmarshal(r.Value, &m); err != nil {
				skipped++
				continue
			}
			ms = []metric.Metric{m}
		}
		for i := range ms {
			if ms[i].AssetID == "" && len(r.Key) > 0 {
				ms[i].AssetID = string(r.Key)
			}
			if ms[i].Timestamp.IsZero() && !r.Timestamp.IsZero() {
				ms[i].Timestamp = r.Timestamp
			}
			if ms[i].Source == "" {
				ms[i].Source = fmt.Sprintf("kafka:%s/%d@%d", r.Topic, r.Partition, r.Offset)
			}
		}
		out = append(out, ms...)
	}
	return out, skipped
}
