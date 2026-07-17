// Package poller (Interval) implements the interval-based scanning strategy.
//
// Objective:
// Provide a high-reliability polling mechanism that performs full directory
// scans at regular intervals. This is the recommended strategy for network
// shares or storage systems that do not reliably emit file system events.
//
// Data Flow:
// 1. Ticker: Triggers a directory scan every N seconds (configured via Poll.Value).
// 2. Scan: Uses OSUtils.GetFiles to retrieve all files in the target directory.
// 3. Safety Check: Verifies non-recursive constraints via OSUtils.HasSubfolders.
// 4. Dispatch: Sends the discovered file slice to the Engine via the results channel.
package poller

import (
	"context"
	"time"

	"criticalsys.net/dirpoller/internal/config"
)

// IntervalPoller discovers files by performing a full directory scan at fixed time steps.
// This is the most reliable algorithm for all storage types (local, network, cloud).
type IntervalPoller struct {
	cfg   *config.Config
	utils OSUtils
}

func NewIntervalPoller(cfg *config.Config) *IntervalPoller {
	return &IntervalPoller{
		cfg:   cfg,
		utils: NewOSUtils(cfg.Poll.MaxBatchSize),
	}
}

// Start begins the polling process and sends discovered files to the channel.
// It blocks until the context is cancelled or a fatal error occurs.
//
// Data Flow:
// 1. Initialization: Parses the interval from configuration.
// 2. Initial Polling: Executes a scan immediately on startup.
// 3. Main Loop: Waits for ticker events or context cancellation.
// 4. Execution: Calls poll() on every ticker tick.
func (p *IntervalPoller) Start(ctx context.Context, results chan<- []string) error {
	var interval int
	switch val := p.cfg.Poll.Value.(type) {
	case int:
		interval = val
	case float64:
		interval = int(val)
	default:
		interval = 60
	}
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	// Initial check
	if err := p.poll(ctx, results); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := p.poll(ctx, results); err != nil {
				return err
			}
		}
	}
}

// poll performs a single scan of the directory. It enforces the non-recursive requirement
// before collecting and sending files to the results channel.
func (p *IntervalPoller) poll(ctx context.Context, results chan<- []string) error {
	files, err := p.utils.GetFiles(p.cfg.Poll.Directory)
	if err != nil {
		return err
	}
	if len(files) > 0 {
		select {
		case results <- files:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
