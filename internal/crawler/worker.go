package crawler

import (
	"context"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/Almahr1/quert/internal/client"
	"github.com/Almahr1/quert/internal/config"
	"github.com/Almahr1/quert/internal/frontier"
	"github.com/Almahr1/quert/internal/robots"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// CrawlJob represents a single crawling task
type CrawlJob struct {
	URL         string
	URLInfo     *frontier.URLInfo
	Priority    int
	Depth       int
	Headers     map[string]string
	RequestID   string
	SubmittedAt time.Time
	Context     context.Context
}

// CrawlResult represents the result of a crawling operation
type CrawlResult struct {
	Job           *CrawlJob
	URL           string
	StatusCode    int
	Headers       http.Header
	Body          []byte
	ContentType   string
	ContentLength int64
	ResponseTime  time.Duration
	Error         error
	Links         []string
	ExtractedText string
	CompletedAt   time.Time
	Success       bool
	Retryable     bool
}

// WorkerStats holds statistics for a worker
type WorkerStats struct {
	WorkerID       int
	JobsProcessed  int64
	JobsSuccessful int64
	JobsFailed     int64
	TotalTime      time.Duration
	AverageTime    time.Duration
	LastJobTime    time.Time
	IsActive       bool
	CurrentJob     *CrawlJob
}

// CrawlerEngine manages the worker pool and job distribution
type CrawlerEngine struct {
	// Configuration
	config     *config.CrawlerConfig
	httpConfig *config.HTTPConfig
	logger     *zap.Logger

	// Worker pool management
	workers     int
	jobs        chan *CrawlJob
	results     chan *CrawlResult
	workerStats map[int]*WorkerStats
	statsMutex  sync.RWMutex

	// HTTP and external dependencies
	httpClient   *client.HTTPClient
	robotsParser *robots.Parser

	// Rate limiting
	globalLimiter *rate.Limiter
	hostLimiters  map[string]*rate.Limiter
	limiterMutex  sync.RWMutex

	// Lifecycle management
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	running      bool
	runningMutex sync.RWMutex

	// Metrics and monitoring
	totalJobs       int64
	successfulJobs  int64
	failedJobs      int64
	startTime       time.Time
	metricsCallback func(*CrawlerMetrics)
}

// CrawlerMetrics holds overall crawler performance metrics
type CrawlerMetrics struct {
	TotalJobs      int64
	SuccessfulJobs int64
	FailedJobs     int64
	JobsPerSecond  float64
	AverageLatency time.Duration
	ActiveWorkers  int
	QueueDepth     int
	Uptime         time.Duration
	ErrorRate      float64
}

// WorkerConfig holds configuration for individual workers
type WorkerConfig struct {
	ID              int
	RequestTimeout  time.Duration
	RetryAttempts   int
	BackoffStrategy string
}

// NewCrawlerEngine creates a new crawler engine instance
// robotsCfg is optional - pass nil to use sensible defaults derived from crawler config
func NewCrawlerEngine(cfg *config.CrawlerConfig, httpCfg *config.HTTPConfig, robotsCfg *config.RobotsConfig, logger *zap.Logger) *CrawlerEngine {
	// 1. Validate input parameters (cfg, httpCfg, logger not nil) * should probably handle this better in the future *
	if cfg == nil {
		panic("crawler config cannot be nil")
	}
	if httpCfg == nil {
		panic("http config cannot be nil")
	}
	if logger == nil {
		panic("logger cannot be nil")
	}

	// 2. Set default values if configuration is incomplete
	crawlerConfig := *cfg // Copy to avoid modifying original

	// Set default/optimal worker count (2-4x CPU cores) * you could set this higher if you have more processing power by default *
	if crawlerConfig.ConcurrentWorkers <= 0 {
		cpuCount := runtime.NumCPU()
		crawlerConfig.ConcurrentWorkers = cpuCount * 3 // 3x CPU cores as default
		if crawlerConfig.ConcurrentWorkers > 50 {
			crawlerConfig.ConcurrentWorkers = 50
		}
		logger.Info("using default worker count", zap.Int("workers", crawlerConfig.ConcurrentWorkers), zap.Int("cpu_cores", cpuCount))
	}

	if crawlerConfig.RequestTimeout <= 0 {
		crawlerConfig.RequestTimeout = 30 * time.Second
		logger.Info("using default request timeout", zap.Duration("timeout", crawlerConfig.RequestTimeout))
	}

	if crawlerConfig.UserAgent == "" {
		crawlerConfig.UserAgent = "Quert/1.0 (+https://github.com/Almahr1/quert)"
		logger.Info("using default user agent", zap.String("user_agent", crawlerConfig.UserAgent))
	}

	if crawlerConfig.MaxPages <= 0 {
		crawlerConfig.MaxPages = 10000 // Reasonable default
		logger.Info("using default max pages", zap.Int("max_pages", crawlerConfig.MaxPages))
	}

	if crawlerConfig.MaxDepth <= 0 {
		crawlerConfig.MaxDepth = 5 // Reasonable default depth
		logger.Info("using default max depth", zap.Int("max_depth", crawlerConfig.MaxDepth))
	}

	// 4. Create buffered channels with appropriate buffer sizes
	// Calculate buffer sizes based on worker count with safety bounds
	jobsBufferSize := crawlerConfig.ConcurrentWorkers * 10
	if jobsBufferSize < 50 {
		jobsBufferSize = 50 // Minimum for small deployments
	}
	if jobsBufferSize > 1000 {
		jobsBufferSize = 1000 // Maximum to prevent excessive memory usage
	}

	resultsBufferSize := crawlerConfig.ConcurrentWorkers * 5
	if resultsBufferSize < 25 {
		resultsBufferSize = 25 // Minimum for small deployments
	}
	if resultsBufferSize > 500 {
		resultsBufferSize = 500 // Maximum to prevent excessive memory usage
	}

	logger.Info("creating buffered channels",
		zap.Int("jobs_buffer", jobsBufferSize),
		zap.Int("results_buffer", resultsBufferSize),
		zap.Int("workers", crawlerConfig.ConcurrentWorkers))

	jobsChannel := make(chan *CrawlJob, jobsBufferSize)
	resultsChannel := make(chan *CrawlResult, resultsBufferSize)

	httpClient := client.NewHTTPClient(httpCfg, logger)

	// 6. Create robots.txt parser instance with configurable settings
	var robotsConfig robots.Config
	if robotsCfg != nil {
		// Map user-provided robots configuration to robots.Config format
		userAgent := robotsCfg.UserAgent
		if userAgent == "" {
			userAgent = crawlerConfig.UserAgent // Fallback to crawler user agent
		}

		cacheDuration := robotsCfg.CacheDuration
		if cacheDuration <= 0 {
			cacheDuration = 24 * time.Hour // Default if not specified
		}

		robotsConfig = robots.Config{
			UserAgent:   userAgent,
			CacheTTL:    cacheDuration,
			HTTPTimeout: crawlerConfig.RequestTimeout, // Always use crawler timeout
			MaxSize:     500 * 1024,                   // Standard 500KB limit
		}
		logger.Info("using provided robots.txt configuration",
			zap.String("user_agent", robotsConfig.UserAgent),
			zap.Duration("cache_ttl", robotsConfig.CacheTTL),
			zap.Bool("enabled", robotsCfg.Enabled),
			zap.Bool("respect_crawl_delay", robotsCfg.RespectCrawlDelay))
	} else {
		// Create sensible defaults derived from crawler config
		robotsConfig = robots.Config{
			UserAgent:   crawlerConfig.UserAgent,      // Reuse crawler's user agent
			CacheTTL:    24 * time.Hour,               // Standard 24-hour cache
			HTTPTimeout: crawlerConfig.RequestTimeout, // Match crawler timeout
			MaxSize:     500 * 1024,                   // 500KB max robots.txt size
		}
		logger.Info("using default robots.txt configuration derived from crawler config",
			zap.String("user_agent", robotsConfig.UserAgent),
			zap.Duration("cache_ttl", robotsConfig.CacheTTL),
			zap.Duration("http_timeout", robotsConfig.HTTPTimeout))
	}

	robotsParser := robots.NewParser(robotsConfig, httpClient)

	// 7. Set up rate limiters (global and per-host maps)
	// Global rate limiter - conservative default of 1 request per second
	globalLimiter := rate.NewLimiter(rate.Limit(1.0), 2) // 1 req/sec, burst of 2

	// Per-host rate limiters map (will be populated dynamically)
	hostLimiters := make(map[string]*rate.Limiter)

	logger.Info("initialized rate limiters",
		zap.Float64("global_rate_limit", float64(globalLimiter.Limit())),
		zap.Int("global_burst", globalLimiter.Burst()))

	// 8. Initialize worker statistics tracking
	workerStats := make(map[int]*WorkerStats)
	for i := 0; i < crawlerConfig.ConcurrentWorkers; i++ {
		workerStats[i] = &WorkerStats{
			WorkerID:       i,
			JobsProcessed:  0,
			JobsSuccessful: 0,
			JobsFailed:     0,
			TotalTime:      0,
			AverageTime:    0,
			LastJobTime:    time.Time{},
			IsActive:       false,
			CurrentJob:     nil,
		}
	}

	// 9. Set up context for graceful shutdown
	// Note: Context will be provided when Start() is called
	// Here we just initialize the engine structure

	// 10. Return configured CrawlerEngine instance
	engine := &CrawlerEngine{
		// Configuration
		config:     &crawlerConfig,
		httpConfig: httpCfg,
		logger:     logger,

		// Worker pool management
		workers:     crawlerConfig.ConcurrentWorkers,
		jobs:        jobsChannel,
		results:     resultsChannel,
		workerStats: workerStats,
		statsMutex:  sync.RWMutex{},

		// HTTP and external dependencies
		httpClient:   httpClient,
		robotsParser: robotsParser,

		// Rate limiting
		globalLimiter: globalLimiter,
		hostLimiters:  hostLimiters,
		limiterMutex:  sync.RWMutex{},

		// Lifecycle management
		ctx:          nil, // Will be set in Start()
		cancel:       nil, // Will be set in Start()
		wg:           sync.WaitGroup{},
		running:      false,
		runningMutex: sync.RWMutex{},

		// Metrics and monitoring
		totalJobs:       0,
		successfulJobs:  0,
		failedJobs:      0,
		startTime:       time.Time{}, // Will be set in Start()
		metricsCallback: nil,
	}

	logger.Info("crawler engine initialized successfully",
		zap.Int("workers", crawlerConfig.ConcurrentWorkers),
		zap.Int("jobs_buffer", jobsBufferSize),
		zap.Int("results_buffer", resultsBufferSize))

	return engine
}

// Start begins the crawler engine and spawns worker goroutines
func (c *CrawlerEngine) Start(ctx context.Context) error {
	// TODO: Start the crawler engine
	// 1. Check if engine is already running (thread-safe check)
	// 2. Set running state to true with mutex protection
	// 3. Store provided context and create cancellable child context
	// 4. Record start time for uptime tracking
	// 5. Spawn configured number of worker goroutines
	// 6. Start result processor goroutine
	// 7. Start metrics collection goroutine (if callback provided)
	// 8. Start rate limiter cleanup goroutine
	// 9. Log successful startup with worker count
	// 10. Return nil on success, error on failure

	return nil
}

// Stop gracefully shuts down the crawler engine
func (c *CrawlerEngine) Stop() error {
	// TODO: Gracefully stop the crawler engine
	// 1. Check if engine is running (return early if not)
	// 2. Set running state to false with mutex protection
	// 3. Cancel context to signal shutdown to all goroutines
	// 4. Close jobs channel to stop accepting new jobs
	// 5. Wait for all workers to finish current jobs (with timeout)
	// 6. Close results channel after workers are done
	// 7. Wait for result processor to finish
	// 8. Clean up rate limiters and release resources
	// 9. Log shutdown completion with final statistics
	// 10. Return nil on success, error on timeout

	return nil
}

// SubmitJob adds a new crawl job to the queue
func (c *CrawlerEngine) SubmitJob(job *CrawlJob) error {
	// TODO: Submit a crawl job to the worker pool
	// 1. Validate job parameters (URL, context not nil)
	// 2. Check if engine is running (return error if not)
	// 3. Set job submission timestamp
	// 4. Generate unique request ID if not provided
	// 5. Check robots.txt permissions for the URL
	// 6. Apply rate limiting for the host
	// 7. Attempt to send job to jobs channel (non-blocking)
	// 8. Increment total jobs counter (thread-safe)
	// 9. Log job submission with URL and priority
	// 10. Return nil on success, error on failure or queue full

	return nil
}

// GetResults returns a channel for receiving crawl results
func (c *CrawlerEngine) GetResults() <-chan *CrawlResult {
	// TODO: Return results channel for consumers
	// 1. Check if engine is initialized
	// 2. Return read-only channel to results
	// Note: This is a simple getter but important for proper channel access

	return nil
}

// GetMetrics returns current crawler performance metrics
func (c *CrawlerEngine) GetMetrics() *CrawlerMetrics {
	// TODO: Calculate and return current metrics
	// 1. Acquire read lock for thread-safe access
	// 2. Calculate jobs per second based on uptime
	// 3. Calculate average latency from worker stats
	// 4. Count active workers currently processing jobs
	// 5. Get current queue depth from jobs channel
	// 6. Calculate error rate percentage
	// 7. Calculate total uptime since start
	// 8. Create and populate CrawlerMetrics struct
	// 9. Release lock and return metrics
	// 10. Handle edge cases (zero values, division by zero)

	return nil
}

// GetWorkerStats returns statistics for all workers
func (c *CrawlerEngine) GetWorkerStats() map[int]*WorkerStats {
	// TODO: Return worker statistics
	// 1. Acquire read lock for thread-safe access
	// 2. Create copy of worker stats map to avoid data races
	// 3. Calculate average times for each worker
	// 4. Update active status based on current job presence
	// 5. Release lock and return stats copy

	return nil
}

// SetMetricsCallback sets a callback function for metrics reporting
func (c *CrawlerEngine) SetMetricsCallback(callback func(*CrawlerMetrics)) {
	// TODO: Set metrics callback for periodic reporting
	// 1. Store callback function for later use
	// 2. If engine is running, restart metrics goroutine with new callback
	// Note: Simple setter but enables monitoring integration

}

// worker is the main worker goroutine function
func (c *CrawlerEngine) worker(ctx context.Context, workerID int) {
	// TODO: Main worker loop for processing crawl jobs
	// 1. Initialize worker statistics and set active status
	// 2. Log worker startup with worker ID
	// 3. Start main processing loop with context cancellation
	// 4. Listen for jobs from jobs channel
	// 5. Process each job by calling processJob method
	// 6. Update worker statistics after each job
	// 7. Handle context cancellation gracefully
	// 8. Log worker shutdown when loop exits
	// 9. Decrement wait group to signal completion
	// 10. Set worker as inactive in statistics

}

// processJob handles the actual crawling of a single URL
func (c *CrawlerEngine) processJob(ctx context.Context, job *CrawlJob, workerID int) *CrawlResult {
	// TODO: Process a single crawl job
	// 1. Record job start time and update worker stats
	// 2. Check robots.txt permissions for the URL
	// 3. Apply rate limiting for the host
	// 4. Create HTTP request with appropriate headers
	// 5. Execute HTTP request with timeout and context
	// 6. Handle HTTP response and read body
	// 7. Extract links from HTML content (if applicable)
	// 8. Extract main text content for LLM training
	// 9. Create CrawlResult with all collected data
	// 10. Handle errors and determine if job is retryable

	return nil
}

// resultProcessor handles crawl results and manages output
func (c *CrawlerEngine) resultProcessor(ctx context.Context) {
	// TODO: Process crawl results from workers
	// 1. Start result processing loop with context cancellation
	// 2. Listen for results from results channel
	// 3. Update global statistics (success/failure counters)
	// 4. Log result processing with URL and status
	// 5. Handle successful results (store data, extract links)
	// 6. Handle failed results (retry logic, error logging)
	// 7. Send results to external processors if configured
	// 8. Clean up resources associated with completed jobs
	// 9. Handle context cancellation and drain remaining results
	// 10. Log processor shutdown when loop exits

}

// metricsCollector periodically collects and reports metrics
func (c *CrawlerEngine) metricsCollector(ctx context.Context) {
	// TODO: Collect and report metrics periodically
	// 1. Create ticker for periodic metrics collection (default: 30s)
	// 2. Start metrics collection loop with context cancellation
	// 3. On each tick, calculate current metrics using GetMetrics
	// 4. Call metrics callback function if configured
	// 5. Log key metrics at INFO level for monitoring
	// 6. Handle context cancellation gracefully
	// 7. Stop ticker and clean up resources
	// 8. Log metrics collector shutdown

}

// getRateLimiter gets or creates a rate limiter for a specific host
func (c *CrawlerEngine) getRateLimiter(host string) *rate.Limiter {
	// 1. Acquire read lock to check if limiter exists
	c.limiterMutex.RLock()
	if limiter, exists := c.hostLimiters[host]; exists {
		c.limiterMutex.RUnlock()
		return limiter
	}
	c.limiterMutex.RUnlock()

	// 3. Upgrade to write lock if limiter doesn't exist
	c.limiterMutex.Lock()
	defer c.limiterMutex.Unlock()

	// 4. Double-check pattern to avoid race condition
	if limiter, exists := c.hostLimiters[host]; exists {
		return limiter
	}

	// 5. Create new rate limiter with host-specific configuration
	// Default: 2 requests per second per host, burst of 3
	// TODO: 8. Handle rate limit configuration from robots.txt crawl-delay
	hostLimiter := rate.NewLimiter(rate.Limit(2.0), 3)

	// 6. Store limiter in map for future use
	c.hostLimiters[host] = hostLimiter

	c.logger.Debug("created new rate limiter for host",
		zap.String("host", host),
		zap.Float64("rate_limit", float64(hostLimiter.Limit())),
		zap.Int("burst", hostLimiter.Burst()))

	return hostLimiter
}

// cleanupRateLimiters removes unused rate limiters to prevent memory leaks
func (c *CrawlerEngine) cleanupRateLimiters(ctx context.Context) {
	// 1. Create ticker for periodic cleanup (default: 10 minutes)
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	c.logger.Info("starting rate limiter cleanup goroutine", zap.Duration("interval", 10*time.Minute))

	// 2. Start cleanup loop with context cancellation
	for {
		select {
		case <-ctx.Done():
			// 6. Handle context cancellation gracefully
			c.logger.Info("rate limiter cleanup goroutine stopping due to context cancellation")
			return

		case <-ticker.C:
			// 3. On each tick, iterate through rate limiters
			c.limiterMutex.Lock()

			initialCount := len(c.hostLimiters)
			removedCount := 0

			// 4. Remove limiters that haven't been used recently
			// Simple approach: remove limiters with no recent activity
			// More sophisticated: track last use time and remove old ones
			for host, limiter := range c.hostLimiters {
				// Check if limiter has available tokens (indicating no recent heavy use)
				// This is a simple heuristic - if burst is fully available, likely unused
				if limiter.Tokens() >= float64(limiter.Burst()) {
					delete(c.hostLimiters, host)
					removedCount++
				}
			}

			c.limiterMutex.Unlock()

			// 5. Log cleanup statistics (removed/remaining limiters)
			if removedCount > 0 {
				c.logger.Info("cleaned up unused rate limiters",
					zap.Int("removed", removedCount),
					zap.Int("remaining", initialCount-removedCount),
					zap.Int("initial", initialCount))
			}
		}
	}
}

// isRunning safely checks if the crawler engine is running
func (c *CrawlerEngine) isRunning() bool {
	// TODO: Thread-safe check of running status
	// 1. Acquire read lock for thread safety
	// 2. Read running status
	// 3. Release lock and return status

	return false
}

// updateWorkerStats safely updates statistics for a worker
func (c *CrawlerEngine) updateWorkerStats(workerID int, job *CrawlJob, result *CrawlResult) {
	// TODO: Update worker statistics thread-safely
	// 1. Acquire write lock for thread safety
	// 2. Get or create worker stats for workerID
	// 3. Increment job counters based on result success/failure
	// 4. Update timing statistics with job duration
	// 5. Calculate running average of job processing time
	// 6. Update last job time and current job reference
	// 7. Release lock after updates complete

}
