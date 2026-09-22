// Command predictmaint runs the predictive-maintenance service: HTTP API,
// optional Kafka ingest, metrics and tracing. main is wiring only.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/udaykishore-resu/predictmaint/internal/adapters/kafka"
	"github.com/udaykishore-resu/predictmaint/internal/adapters/memory"
	"github.com/udaykishore-resu/predictmaint/internal/adapters/postgres"
	apihttp "github.com/udaykishore-resu/predictmaint/internal/api/http"
	"github.com/udaykishore-resu/predictmaint/internal/config"
	"github.com/udaykishore-resu/predictmaint/internal/domain/cmms"
	"github.com/udaykishore-resu/predictmaint/internal/domain/engine"
	"github.com/udaykishore-resu/predictmaint/internal/domain/metric"
	"github.com/udaykishore-resu/predictmaint/internal/observability"
	"github.com/udaykishore-resu/predictmaint/internal/ports"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "predictmaint:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	log := observability.NewLogger(os.Stdout, cfg.LogFormat, cfg.LogLevel, cfg.ServiceName, cfg.Environment)
	log.Info("starting", "version", version, "http_addr", cfg.HTTPAddr, "store", cfg.StoreBackend, "kafka", cfg.KafkaEnabled)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	metrics := observability.NewMetrics("predictmaint")
	tracer, shutdownTracing, err := observability.SetupTracing(ctx, observability.TracingConfig{
		ServiceName: cfg.ServiceName, Environment: cfg.Environment, Version: version,
		Endpoint: cfg.OTLPEndpoint, Insecure: cfg.OTLPInsecure, Ratio: cfg.TraceRatio,
	})
	if err != nil {
		return fmt.Errorf("tracing: %w", err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(c)
	}()

	var store ports.Store
	switch cfg.StoreBackend {
	case "postgres":
		pg, err := postgres.Open(ctx, cfg.PostgresDSN)
		if err != nil {
			return err
		}
		if err := pg.Migrate(ctx, cfg.MigrationsDir); err != nil {
			return err
		}
		store = pg
	default:
		store = memory.New()
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.Error("store close", "err", err)
		}
	}()

	simCMMS := cmms.NewSimulated()
	eng, err := engine.New(ctx, engine.Options{
		Store: store, CMMS: simCMMS, Logger: log, Observer: metrics, MaxWorkOrderRetries: cfg.WorkOrderRetries,
	})
	if err != nil {
		return err
	}

	readiness := []apihttp.ReadinessCheck{{Name: "store", Check: store.Ping}}
	g, gctx := errgroup.WithContext(ctx)

	if cfg.KafkaEnabled {
		consumer, err := kafka.New(kafka.Config{
			Brokers: cfg.KafkaBrokers, Topic: cfg.KafkaTopic, GroupID: cfg.KafkaGroup, ClientID: cfg.ServiceName,
		}, log, metrics)
		if err != nil {
			return err
		}
		readiness = append(readiness, apihttp.ReadinessCheck{Name: "kafka", Check: consumer.Ping})
		g.Go(func() error {
			log.Info("kafka consuming", "topic", cfg.KafkaTopic, "group", cfg.KafkaGroup, "brokers", cfg.KafkaBrokers)
			return consumer.Run(gctx, func(ctx context.Context, batch []metric.Metric) error {
				res, err := eng.Ingest(ctx, batch)
				if err != nil {
					return err
				}
				if res.Rejected > 0 {
					log.Warn("kafka batch partially rejected", "rejected", res.Rejected, "accepted", res.Accepted, "errors", res.Errors)
				}
				return nil
			})
		})
	}

	srv := apihttp.New(apihttp.Options{
		Engine: eng, Store: store, Logger: log, Metrics: metrics, Tracer: tracer,
		MaxBodyBytes: cfg.MaxBodyBytes, Readiness: readiness, Version: version,
	})
	g.Go(func() error {
		return apihttp.ListenAndServe(gctx, cfg.HTTPAddr, srv.Handler(), cfg.ReadTimeout, cfg.WriteTimeout, cfg.ShutdownTimeout, log)
	})

	// Programme gauges refresh: alerts/day, precision proxy, MTTA, assets.
	g.Go(func() error {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-gctx.Done():
				return nil
			case <-t.C:
				if st, err := eng.Stats(gctx, ""); err == nil {
					metrics.SetStats(st)
				}
				metrics.SetAssetsTracked(len(eng.Assets("")))
			}
		}
	})

	err = g.Wait()
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("stopped")
	return nil
}
