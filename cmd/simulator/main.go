// Command simulator streams a bearing-degradation trajectory for a small
// pump fleet into a running predictmaint instance and prints what the
// engine does with it: risk rising on the bad pump, one alert, one work
// order. It is the README demo and doubles as a load/smoke tool.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/udaykishore-resu/predictmaint/internal/domain/alerting"
	"github.com/udaykishore-resu/predictmaint/internal/domain/engine"
	"github.com/udaykishore-resu/predictmaint/internal/sim"
)

func main() {
	cfg := sim.Defaults()
	var (
		url     = flag.String("url", "http://localhost:8080", "predictmaint base URL")
		steps   = flag.Int("steps", 160, "number of 5-minute fleet snapshots to send")
		pumps   = flag.Int("pumps", cfg.Pumps, "number of pumps")
		badPump = flag.Int("bad-pump", 3, "1-based index of the pump that degrades (0 = none)")
		from    = flag.Int("degrade-from", cfg.DegradeFrom, "step at which degradation starts")
		ramp    = flag.Int("ramp", cfg.RampSteps, "steps to full severity")
		site    = flag.String("site", cfg.Site, "site name")
		seed    = flag.Int64("seed", cfg.Seed, "random seed")
		delay   = flag.Duration("delay", 0, "pause between snapshots (0 = as fast as possible)")
		quiet   = flag.Bool("quiet", false, "only print milestones")
	)
	flag.Parse()

	cfg.Pumps, cfg.DegradeFrom, cfg.RampSteps, cfg.Site, cfg.Seed = *pumps, *from, *ramp, *site, *seed
	cfg.Degrading = nil
	if *badPump > 0 && *badPump <= *pumps {
		cfg.Degrading = []int{*badPump - 1}
	}
	if err := run(cfg, *url, *steps, *delay, *quiet); err != nil {
		fmt.Fprintln(os.Stderr, "simulator:", err)
		os.Exit(1)
	}
}

func run(cfg sim.Config, base string, steps int, delay time.Duration, quiet bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	client := &http.Client{Timeout: 30 * time.Second}
	s := sim.New(cfg)
	bad := ""
	if len(cfg.Degrading) > 0 {
		bad = s.AssetID(cfg.Degrading[0])
	}
	fmt.Printf("simulating %d pumps at %s, %d snapshots x %s", cfg.Pumps, cfg.Site, steps, cfg.StepInterval)
	if bad != "" {
		fmt.Printf("; %s develops a bearing fault from step %d", bad, cfg.DegradeFrom)
	}
	fmt.Println()

	var totalAlerts, totalWOs int
	for i := 0; i < steps; i++ {
		if err := ctx.Err(); err != nil {
			return nil
		}
		batch := s.Next()
		res, err := post(ctx, client, base+"/v1/metrics", batch)
		if err != nil {
			return fmt.Errorf("step %d: %w", i, err)
		}
		totalAlerts += res.Alerts
		totalWOs += res.WorkOrders
		if res.Rejected > 0 {
			fmt.Printf("step %3d: %d metrics rejected: %v\n", i, res.Rejected, res.Errors)
		}
		if res.Alerts > 0 {
			fmt.Printf("step %3d: ALERT fired (%d), work orders created: %d\n", i, res.Alerts, res.WorkOrders)
		}
		if !quiet && bad != "" && (i%10 == 0 || res.Alerts > 0) {
			h, err := health(ctx, client, base, bad)
			if err == nil {
				fmt.Printf("step %3d: t=%s %s severity=%.2f risk=%.3f anomaly=%.3f drift=%.3f state=%s baseline=%s threshold=%.2f\n",
					i, s.Time().Add(-cfg.StepInterval).Format("15:04"), bad, s.Severity(cfg.Degrading[0]), h.Risk, h.Anomaly, h.Drift, h.State, h.BaselineSource, h.EffectiveThreshold)
			}
		}
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil
			}
		}
	}
	fmt.Printf("done: %d snapshots, %d alerts, %d work orders\n", steps, totalAlerts, totalWOs)
	if bad != "" {
		alerts, err := listAlerts(ctx, client, base, cfg.Site)
		if err == nil {
			for _, a := range alerts {
				fmt.Printf("alert %s asset=%s failure_mode=%s risk=%.3f confidence=%.3f work_order=%s status=%s\n",
					a.ID, a.AssetID, a.FailureMode, a.Risk, a.Confidence, a.WorkOrderID, a.Status)
			}
		}
	}
	return nil
}

func post(ctx context.Context, c *http.Client, url string, body any) (engine.IngestResult, error) {
	var res engine.IngestResult
	buf, err := json.Marshal(body)
	if err != nil {
		return res, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return res, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return res, err
	}
	defer func() { _ = resp.Body.Close() }() // close error on a read-only response body is not actionable
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return res, fmt.Errorf("POST %s: %s: %s", url, resp.Status, string(data))
	}
	return res, json.Unmarshal(data, &res)
}

func health(ctx context.Context, c *http.Client, base, asset string) (engine.Health, error) {
	var h engine.Health
	err := getJSON(ctx, c, base+"/v1/assets/"+asset+"/health", &h)
	return h, err
}

func listAlerts(ctx context.Context, c *http.Client, base, site string) ([]alerting.Alert, error) {
	var out struct {
		Alerts []alerting.Alert `json:"alerts"`
	}
	err := getJSON(ctx, c, base+"/v1/alerts?site="+site, &out)
	return out.Alerts, err
}

func getJSON(ctx context.Context, c *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }() // close error on a read-only response body is not actionable
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
