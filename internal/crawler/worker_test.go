package crawler

import (
	"context"
	"testing"
	"time"

	"github.com/Almahr1/quert/internal/config"
	"go.uber.org/zap"
)

func TestNewCrawlerEngine(t *testing.T) {
	// Create test configurations
	crawlerConfig := &config.CrawlerConfig{
		MaxPages:          1000,
		MaxDepth:          3,
		ConcurrentWorkers: 4,
		RequestTimeout:    10 * time.Second,
		UserAgent:         "Test-Crawler/1.0",
	}

	httpConfig := &config.HTTPConfig{
		MaxIdleConnections:        100,
		MaxIdleConnectionsPerHost: 10,
		IdleConnectionTimeout:     30 * time.Second,
		DisableKeepAlives:         false,
		Timeout:                   10 * time.Second,
		DialTimeout:               5 * time.Second,
		TlsHandshakeTimeout:       5 * time.Second,
		ResponseHeaderTimeout:     5 * time.Second,
		DisableCompression:        false,
		AcceptEncoding:            "gzip, deflate",
	}

	robotsConfig := &config.RobotsConfig{
		Enabled:            true,
		CacheDuration:      1 * time.Hour,
		UserAgent:          "Test-Crawler/1.0",
		CrawlDelayOverride: false,
		RespectCrawlDelay:  true,
	}

	logger, _ := zap.NewDevelopment()

	// Test engine creation
	engine := NewCrawlerEngine(crawlerConfig, httpConfig, robotsConfig, logger)

	// Verify engine is properly initialized
	if engine == nil {
		t.Fatal("Expected CrawlerEngine to be created, got nil")
	}

	if engine.Workers != 4 {
		t.Errorf("Expected 4 workers, got %d", engine.Workers)
	}

	if engine.Config.UserAgent != "Test-Crawler/1.0" {
		t.Errorf("Expected user agent 'Test-Crawler/1.0', got %s", engine.Config.UserAgent)
	}

	if engine.IsRunning() {
		t.Error("Expected engine to not be running initially")
	}

	if engine.HTTPClient == nil {
		t.Error("Expected HTTP client to be initialized")
	}

	if engine.RobotsParser == nil {
		t.Error("Expected robots parser to be initialized")
	}

	if len(engine.WorkerStats) != 4 {
		t.Errorf("Expected 4 worker stats entries, got %d", len(engine.WorkerStats))
	}
}

func TestCrawlerEngineStartStop(t *testing.T) {
	// Create minimal test configuration
	crawlerConfig := &config.CrawlerConfig{
		MaxPages:          10,
		MaxDepth:          1,
		ConcurrentWorkers: 2,
		RequestTimeout:    5 * time.Second,
		UserAgent:         "Test-Crawler/1.0",
	}

	httpConfig := &config.HTTPConfig{
		MaxIdleConnections:        10,
		MaxIdleConnectionsPerHost: 5,
		IdleConnectionTimeout:     10 * time.Second,
		DisableKeepAlives:         false,
		Timeout:                   5 * time.Second,
		DialTimeout:               2 * time.Second,
		TlsHandshakeTimeout:       2 * time.Second,
		ResponseHeaderTimeout:     2 * time.Second,
		DisableCompression:        false,
		AcceptEncoding:            "gzip",
	}

	logger, _ := zap.NewDevelopment()

	engine := NewCrawlerEngine(crawlerConfig, httpConfig, nil, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Test starting the engine
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Failed to start engine: %v", err)
	}

	if !engine.IsRunning() {
		t.Error("Expected engine to be running after start")
	}

	// Let it run briefly
	time.Sleep(100 * time.Millisecond)

	// Test stopping the engine
	if err := engine.Stop(); err != nil {
		t.Fatalf("Failed to stop engine: %v", err)
	}

	if engine.IsRunning() {
		t.Error("Expected engine to not be running after stop")
	}

	// Test that we can't start an already stopped engine that's been started before
	// (This tests the lifecycle)
}

func TestGetMetrics(t *testing.T) {
	crawlerConfig := &config.CrawlerConfig{
		ConcurrentWorkers: 2,
		RequestTimeout:    5 * time.Second,
		UserAgent:         "Test-Crawler/1.0",
	}

	httpConfig := &config.HTTPConfig{
		Timeout: 5 * time.Second,
	}

	logger, _ := zap.NewDevelopment()

	engine := NewCrawlerEngine(crawlerConfig, httpConfig, nil, logger)

	// Test metrics before starting
	metrics := engine.GetMetrics()
	if metrics == nil {
		t.Fatal("Expected metrics to be returned, got nil")
	}

	if metrics.TotalJobs != 0 {
		t.Errorf("Expected 0 total jobs initially, got %d", metrics.TotalJobs)
	}

	if metrics.ActiveWorkers != 0 {
		t.Errorf("Expected 0 active workers initially, got %d", metrics.ActiveWorkers)
	}

	if metrics.QueueDepth < 0 {
		t.Errorf("Expected non-negative queue depth, got %d", metrics.QueueDepth)
	}
}

func TestGetWorkerStats(t *testing.T) {
	crawlerConfig := &config.CrawlerConfig{
		ConcurrentWorkers: 3,
		RequestTimeout:    5 * time.Second,
		UserAgent:         "Test-Crawler/1.0",
	}

	httpConfig := &config.HTTPConfig{
		Timeout: 5 * time.Second,
	}

	logger, _ := zap.NewDevelopment()

	engine := NewCrawlerEngine(crawlerConfig, httpConfig, nil, logger)

	stats := engine.GetWorkerStats()
	if stats == nil {
		t.Fatal("Expected worker stats to be returned, got nil")
	}

	if len(stats) != 3 {
		t.Errorf("Expected 3 worker stats, got %d", len(stats))
	}

	// Check that each worker has proper initial stats
	for i := 0; i < 3; i++ {
		workerStat, exists := stats[i]
		if !exists {
			t.Errorf("Expected worker %d stats to exist", i)
			continue
		}

		if workerStat.WorkerID != i {
			t.Errorf("Expected worker ID %d, got %d", i, workerStat.WorkerID)
		}

		if workerStat.JobsProcessed != 0 {
			t.Errorf("Expected 0 jobs processed initially for worker %d, got %d", i, workerStat.JobsProcessed)
		}

		if workerStat.IsActive {
			t.Errorf("Expected worker %d to be inactive initially", i)
		}
	}
}

func TestGetRateLimiter(t *testing.T) {
	crawlerConfig := &config.CrawlerConfig{
		ConcurrentWorkers: 1,
		RequestTimeout:    5 * time.Second,
		UserAgent:         "Test-Crawler/1.0",
	}

	httpConfig := &config.HTTPConfig{
		Timeout: 5 * time.Second,
	}

	logger, _ := zap.NewDevelopment()

	engine := NewCrawlerEngine(crawlerConfig, httpConfig, nil, logger)

	// Test creating rate limiter for a host
	host := "example.com"
	limiter1 := engine.GetRateLimiter(host)

	if limiter1 == nil {
		t.Fatal("Expected rate limiter to be created, got nil")
	}

	// Test that the same host returns the same limiter
	limiter2 := engine.GetRateLimiter(host)

	if limiter1 != limiter2 {
		t.Error("Expected same rate limiter instance for the same host")
	}

	// Test different host gets different limiter
	limiter3 := engine.GetRateLimiter("different.com")

	if limiter1 == limiter3 {
		t.Error("Expected different rate limiter for different host")
	}
}
