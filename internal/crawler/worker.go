package crawler

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Almahr1/quert/internal/client"
	"github.com/Almahr1/quert/internal/config"
	"github.com/Almahr1/quert/internal/extractor"
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
	Job              *CrawlJob
	URL              string
	StatusCode       int
	Headers          http.Header
	Body             []byte
	ContentType      string
	ContentLength    int64
	ResponseTime     time.Duration
	Error            error
	Links            []string
	ExtractedText    string
	ExtractedContent *extractor.ExtractedContent
	CompletedAt      time.Time
	Success          bool
	Retryable        bool
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
	Config     *config.CrawlerConfig
	HTTPConfig *config.HTTPConfig
	Logger     *zap.Logger

	// Worker pool management
	Workers     int
	Jobs        chan *CrawlJob
	Results     chan *CrawlResult
	WorkerStats map[int]*WorkerStats
	StatsMutex  sync.RWMutex

	// HTTP and external dependencies
	HTTPClient       *client.HTTPClient
	RobotsParser     *robots.Parser
	ExtractorFactory *extractor.ExtractorFactory

	// Rate limiting
	GlobalLimiter *rate.Limiter
	HostLimiters  map[string]*rate.Limiter
	LimiterMutex  sync.RWMutex

	// Lifecycle management
	Ctx          context.Context
	Cancel       context.CancelFunc
	Wg           sync.WaitGroup
	Running      bool
	RunningMutex sync.RWMutex

	// Metrics and monitoring
	TotalJobs       int64
	SuccessfulJobs  int64
	FailedJobs      int64
	StartTime       time.Time
	MetricsCallback func(*CrawlerMetrics)
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

func NewCrawlerEngine(cfg *config.CrawlerConfig, httpCfg *config.HTTPConfig, robotsCfg *config.RobotsConfig, logger *zap.Logger) *CrawlerEngine {
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

	// 7. Create content extractor factory with default configuration
	extractorConfig := extractor.GetDefaultExtractorConfig()
	extractorFactory := extractor.NewExtractorFactory(extractorConfig, logger)

	// 8. Set up rate limiters (global and per-host maps)
	// Use configurable rate limits with sensible defaults
	GlobalRateLimit := crawlerConfig.GlobalRateLimit
	if GlobalRateLimit <= 0 {
		GlobalRateLimit = 5.0 // Default 5 req/sec if not configured
	}
	GlobalBurst := crawlerConfig.GlobalBurst
	if GlobalBurst <= 0 {
		GlobalBurst = 10 // Default burst of 10 if not configured
	}

	GlobalLimiter := rate.NewLimiter(rate.Limit(GlobalRateLimit), GlobalBurst)

	// Per-host rate limiters map (will be populated dynamically)
	HostLimiters := make(map[string]*rate.Limiter)

	logger.Info("initialized rate limiters",
		zap.Float64("global_rate_limit", float64(GlobalLimiter.Limit())),
		zap.Int("global_burst", GlobalLimiter.Burst()),
		zap.Float64("per_host_rate_limit", crawlerConfig.PerHostRateLimit),
		zap.Int("per_host_burst", crawlerConfig.PerHostBurst))

	// 9. Initialize worker statistics tracking
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

	// 10. Set up context for graceful shutdown
	// Note: Context will be provided when Start() is called
	// Here we just initialize the engine structure

	// 11. Return configured CrawlerEngine instance
	engine := &CrawlerEngine{
		// Configuration
		Config:     &crawlerConfig,
		HTTPConfig: httpCfg,
		Logger:     logger,

		// Worker pool management
		Workers:     crawlerConfig.ConcurrentWorkers,
		Jobs:        jobsChannel,
		Results:     resultsChannel,
		WorkerStats: workerStats,
		StatsMutex:  sync.RWMutex{},

		// HTTP and external dependencies
		HTTPClient:       httpClient,
		RobotsParser:     robotsParser,
		ExtractorFactory: extractorFactory,

		// Rate limiting
		GlobalLimiter: GlobalLimiter,
		HostLimiters:  HostLimiters,
		LimiterMutex:  sync.RWMutex{},

		// Lifecycle management
		Ctx:          nil, // Will be set in Start()
		Cancel:       nil, // Will be set in Start()
		Wg:           sync.WaitGroup{},
		Running:      false,
		RunningMutex: sync.RWMutex{},

		// Metrics and monitoring
		TotalJobs:       0,
		SuccessfulJobs:  0,
		FailedJobs:      0,
		StartTime:       time.Time{}, // Will be set in Start()
		MetricsCallback: nil,
	}

	logger.Info("crawler engine initialized successfully",
		zap.Int("workers", crawlerConfig.ConcurrentWorkers),
		zap.Int("jobs_buffer", jobsBufferSize),
		zap.Int("results_buffer", resultsBufferSize))

	return engine
}

// Start begins the crawler engine and spawns worker goroutines
func (CrawlerEngine *CrawlerEngine) Start(Context context.Context) error {
	// 1. Check if engine is already running (thread-safe check)
	if CrawlerEngine.IsRunning() {
		return fmt.Errorf("crawler engine is already running")
	}

	// 2. Set running state to true with mutex protection
	CrawlerEngine.RunningMutex.Lock()
	CrawlerEngine.Running = true
	CrawlerEngine.RunningMutex.Unlock()

	// 3. Store provided context and create cancellable child context
	CrawlerEngine.Ctx, CrawlerEngine.Cancel = context.WithCancel(Context)

	// 4. Record start time for uptime tracking
	CrawlerEngine.StartTime = time.Now()

	// 5. Spawn configured number of worker goroutines
	for WorkerID := 0; WorkerID < CrawlerEngine.Workers; WorkerID++ {
		CrawlerEngine.Wg.Add(1)
		go CrawlerEngine.Worker(CrawlerEngine.Ctx, WorkerID)
	}

	// 6. Start result processor goroutine
	CrawlerEngine.Wg.Add(1)
	go CrawlerEngine.ResultProcessor(CrawlerEngine.Ctx)

	// 7. Start metrics collection goroutine (if callback provided)
	if CrawlerEngine.MetricsCallback != nil {
		CrawlerEngine.Wg.Add(1)
		go CrawlerEngine.MetricsCollector(CrawlerEngine.Ctx)
	}

	// 8. Start rate limiter cleanup goroutine
	CrawlerEngine.Wg.Add(1)
	go CrawlerEngine.cleanupRateLimiters(CrawlerEngine.Ctx)

	// 9. Log successful startup with worker count
	CrawlerEngine.Logger.Info("crawler engine started successfully",
		zap.Int("workers", CrawlerEngine.Workers),
		zap.Int("jobs_buffer_size", cap(CrawlerEngine.Jobs)),
		zap.Int("results_buffer_size", cap(CrawlerEngine.Results)))

	// 10. Return nil on success, error on failure
	return nil
}

// Stop gracefully shuts down the crawler engine
func (CrawlerEngine *CrawlerEngine) Stop() error {
	// 1. Check if engine is running (return early if not)
	if !CrawlerEngine.IsRunning() {
		return fmt.Errorf("crawler engine is not running")
	}

	// 2. Set running state to false with mutex protection
	CrawlerEngine.RunningMutex.Lock()
	CrawlerEngine.Running = false
	CrawlerEngine.RunningMutex.Unlock()

	CrawlerEngine.Logger.Info("stopping crawler engine gracefully")

	// 3. Cancel context to signal shutdown to all goroutines
	if CrawlerEngine.Cancel != nil {
		CrawlerEngine.Cancel()
	}

	// 4. Close jobs channel to stop accepting new jobs
	close(CrawlerEngine.Jobs)

	// 5. Wait for all workers to finish current jobs (with timeout)
	WaitDone := make(chan struct{})
	go func() {
		CrawlerEngine.Wg.Wait()
		close(WaitDone)
	}()

	select {
	case <-WaitDone:
		CrawlerEngine.Logger.Info("all workers finished gracefully")
	case <-time.After(30 * time.Second):
		CrawlerEngine.Logger.Warn("timeout waiting for workers to finish, forcing shutdown")
	}

	// 6. Close results channel after workers are done
	close(CrawlerEngine.Results)

	// 7. Wait for result processor to finish (already covered by Wg.Wait above)

	// 8. Clean up rate limiters and release resources
	CrawlerEngine.LimiterMutex.Lock()
	for Host := range CrawlerEngine.HostLimiters {
		delete(CrawlerEngine.HostLimiters, Host)
	}
	CrawlerEngine.LimiterMutex.Unlock()

	// 9. Log shutdown completion with final statistics
	Metrics := CrawlerEngine.GetMetrics()
	if Metrics != nil {
		CrawlerEngine.Logger.Info("crawler engine stopped",
			zap.Int64("total_jobs", Metrics.TotalJobs),
			zap.Int64("successful_jobs", Metrics.SuccessfulJobs),
			zap.Int64("failed_jobs", Metrics.FailedJobs),
			zap.Duration("uptime", Metrics.Uptime))
	}

	// 10. Return nil on success, error on timeout
	return nil
}

// SubmitJob adds a new crawl job to the queue
func (CrawlerEngine *CrawlerEngine) SubmitJob(Job *CrawlJob) error {
	// 1. Validate job parameters (URL, context not nil)
	if Job == nil {
		return fmt.Errorf("job cannot be nil")
	}
	if Job.URL == "" {
		return fmt.Errorf("job URL cannot be empty")
	}
	if Job.Context == nil {
		Job.Context = context.Background()
	}

	// 2. Check if engine is running (return error if not)
	if !CrawlerEngine.IsRunning() {
		return fmt.Errorf("crawler engine is not running")
	}

	// 3. Set job submission timestamp
	Job.SubmittedAt = time.Now()

	// 4. Generate unique request ID if not provided
	if Job.RequestID == "" {
		Job.RequestID = fmt.Sprintf("%d-%d", time.Now().UnixNano(), rand.Int63())
	}

	// 5. Check robots.txt permissions for the URL
	HostFromURL, ExtractErr := frontier.ExtractHostFromURL(Job.URL)
	if ExtractErr != nil {
		return fmt.Errorf("failed to extract host from URL: %w", ExtractErr)
	}

	PermissionResult, RobotsErr := CrawlerEngine.RobotsParser.IsAllowed(Job.Context, Job.URL)
	if RobotsErr != nil {
		CrawlerEngine.Logger.Warn("robots.txt check failed, allowing by default",
			zap.String("url", Job.URL),
			zap.Error(RobotsErr))
	} else if !PermissionResult.Allowed {
		return fmt.Errorf("URL disallowed by robots.txt: %s", Job.URL)
	}

	// 6. Apply rate limiting for the host
	HostLimiter := CrawlerEngine.GetRateLimiter(HostFromURL)
	if GlobalErr := CrawlerEngine.GlobalLimiter.Wait(Job.Context); GlobalErr != nil {
		return fmt.Errorf("global rate limit wait failed: %w", GlobalErr)
	}
	if HostErr := HostLimiter.Wait(Job.Context); HostErr != nil {
		return fmt.Errorf("host rate limit wait failed: %w", HostErr)
	}

	// 7. Attempt to send job to jobs channel (non-blocking)
	select {
	case CrawlerEngine.Jobs <- Job:
		// Successfully queued
	case <-Job.Context.Done():
		return Job.Context.Err()
	default:
		return fmt.Errorf("job queue is full, cannot accept new jobs")
	}

	// 8. Increment total jobs counter (thread-safe)
	atomic.AddInt64(&CrawlerEngine.TotalJobs, 1)

	// 9. Log job submission with URL and priority
	CrawlerEngine.Logger.Debug("job submitted to queue",
		zap.String("url", Job.URL),
		zap.String("request_id", Job.RequestID),
		zap.Int("priority", Job.Priority),
		zap.Int("depth", Job.Depth))

	// 10. Return nil on success, error on failure or queue full
	return nil
}

// GetResults returns a channel for receiving crawl results
func (CrawlerEngine *CrawlerEngine) GetResults() <-chan *CrawlResult {
	// 1. Check if engine is initialized
	if CrawlerEngine == nil {
		return nil
	}

	// 2. Return read-only channel to results
	// Note: This is a simple getter but important for proper channel access
	return CrawlerEngine.Results
}

// GetMetrics returns current crawler performance metrics
func (CrawlerEngine *CrawlerEngine) GetMetrics() *CrawlerMetrics {
	// 1. Acquire read lock for thread-safe access
	CrawlerEngine.StatsMutex.RLock()
	defer CrawlerEngine.StatsMutex.RUnlock()

	// Handle case where engine hasn't started yet
	if CrawlerEngine.StartTime.IsZero() {
		return &CrawlerMetrics{
			TotalJobs:      0,
			SuccessfulJobs: 0,
			FailedJobs:     0,
			JobsPerSecond:  0,
			AverageLatency: 0,
			ActiveWorkers:  0,
			QueueDepth:     len(CrawlerEngine.Jobs),
			Uptime:         0,
			ErrorRate:      0,
		}
	}

	// 2. Calculate jobs per second based on uptime
	TotalJobsSnapshot := atomic.LoadInt64(&CrawlerEngine.TotalJobs)
	SuccessfulJobsSnapshot := atomic.LoadInt64(&CrawlerEngine.SuccessfulJobs)
	FailedJobsSnapshot := atomic.LoadInt64(&CrawlerEngine.FailedJobs)

	// 7. Calculate total uptime since start
	Uptime := time.Since(CrawlerEngine.StartTime)
	UptimeSeconds := Uptime.Seconds()

	var JobsPerSecond float64
	if UptimeSeconds > 0 {
		JobsPerSecond = float64(TotalJobsSnapshot) / UptimeSeconds
	}

	// 3. Calculate average latency from worker stats
	var TotalLatency time.Duration
	var LatencyCount int64
	ActiveWorkerCount := 0

	for _, WorkerStats := range CrawlerEngine.WorkerStats {
		if WorkerStats.IsActive {
			ActiveWorkerCount++
		}
		if WorkerStats.JobsProcessed > 0 {
			TotalLatency += WorkerStats.AverageTime * time.Duration(WorkerStats.JobsProcessed)
			LatencyCount += WorkerStats.JobsProcessed
		}
	}

	var AverageLatency time.Duration
	if LatencyCount > 0 {
		AverageLatency = TotalLatency / time.Duration(LatencyCount)
	}

	// 5. Get current queue depth from jobs channel
	QueueDepth := len(CrawlerEngine.Jobs)

	// 6. Calculate error rate percentage
	var ErrorRate float64
	if TotalJobsSnapshot > 0 {
		ErrorRate = float64(FailedJobsSnapshot) / float64(TotalJobsSnapshot) * 100
	}

	// 8. Create and populate CrawlerMetrics struct
	Metrics := &CrawlerMetrics{
		TotalJobs:      TotalJobsSnapshot,
		SuccessfulJobs: SuccessfulJobsSnapshot,
		FailedJobs:     FailedJobsSnapshot,
		JobsPerSecond:  JobsPerSecond,
		AverageLatency: AverageLatency,
		ActiveWorkers:  ActiveWorkerCount,
		QueueDepth:     QueueDepth,
		Uptime:         Uptime,
		ErrorRate:      ErrorRate,
	}

	// 9. Release lock and return metrics (handled by defer)
	return Metrics
}

// GetWorkerStats returns statistics for all workers
func (CrawlerEngine *CrawlerEngine) GetWorkerStats() map[int]*WorkerStats {
	// 1. Acquire read lock for thread-safe access
	CrawlerEngine.StatsMutex.RLock()
	defer CrawlerEngine.StatsMutex.RUnlock()

	// 2. Create copy of worker stats map to avoid data races
	StatsCopy := make(map[int]*WorkerStats)

	for WorkerID, OriginalStats := range CrawlerEngine.WorkerStats {
		// Create a deep copy of the worker stats
		StatsCopy[WorkerID] = &WorkerStats{
			WorkerID:       OriginalStats.WorkerID,
			JobsProcessed:  OriginalStats.JobsProcessed,
			JobsSuccessful: OriginalStats.JobsSuccessful,
			JobsFailed:     OriginalStats.JobsFailed,
			TotalTime:      OriginalStats.TotalTime,
			LastJobTime:    OriginalStats.LastJobTime,
			IsActive:       OriginalStats.IsActive,
			CurrentJob:     OriginalStats.CurrentJob, // Note: this is a pointer copy
		}

		// 3. Calculate average times for each worker
		if OriginalStats.JobsProcessed > 0 {
			StatsCopy[WorkerID].AverageTime = OriginalStats.TotalTime / time.Duration(OriginalStats.JobsProcessed)
		} else {
			StatsCopy[WorkerID].AverageTime = 0
		}

		// 4. Update active status based on current job presence
		StatsCopy[WorkerID].IsActive = OriginalStats.CurrentJob != nil
	}

	// 5. Release lock and return stats copy (handled by defer)
	return StatsCopy
}

// SetMetricsCallback sets a callback function for metrics reporting
func (CrawlerEngine *CrawlerEngine) SetMetricsCallback(Callback func(*CrawlerMetrics)) {
	// 1. Store callback function for later use
	CrawlerEngine.MetricsCallback = Callback

	// 2. If engine is running, restart metrics goroutine with new callback
	// Note: For simplicity, we'll let the existing metrics goroutine pick up the new callback
	// In a more sophisticated implementation, we might restart the goroutine
	if CrawlerEngine.IsRunning() && Callback != nil {
		CrawlerEngine.Logger.Info("metrics callback updated while engine is running")
	}
}

// Worker is the main worker goroutine function
func (CrawlerEngine *CrawlerEngine) Worker(Context context.Context, WorkerID int) {
	// 9. Decrement wait group to signal completion
	defer CrawlerEngine.Wg.Done()

	// 1. Initialize worker statistics and set active status
	CrawlerEngine.StatsMutex.Lock()
	if CrawlerEngine.WorkerStats[WorkerID] != nil {
		CrawlerEngine.WorkerStats[WorkerID].IsActive = true
	}
	CrawlerEngine.StatsMutex.Unlock()

	// 2. Log worker startup with worker ID
	CrawlerEngine.Logger.Info("worker started",
		zap.Int("worker_id", WorkerID))

	// 10. Set worker as inactive in statistics when function exits
	defer func() {
		CrawlerEngine.StatsMutex.Lock()
		if CrawlerEngine.WorkerStats[WorkerID] != nil {
			CrawlerEngine.WorkerStats[WorkerID].IsActive = false
			CrawlerEngine.WorkerStats[WorkerID].CurrentJob = nil
		}
		CrawlerEngine.StatsMutex.Unlock()

		// 8. Log worker shutdown when loop exits
		CrawlerEngine.Logger.Info("worker stopped",
			zap.Int("worker_id", WorkerID))
	}()

	// 3. Start main processing loop with context cancellation
	for {
		select {
		case <-Context.Done():
			// 7. Handle context cancellation gracefully
			CrawlerEngine.Logger.Debug("worker received shutdown signal",
				zap.Int("worker_id", WorkerID))
			return

		case Job, ChannelOpen := <-CrawlerEngine.Jobs:
			// 4. Listen for jobs from jobs channel
			if !ChannelOpen {
				// Jobs channel closed, shutdown gracefully
				CrawlerEngine.Logger.Debug("jobs channel closed, worker shutting down",
					zap.Int("worker_id", WorkerID))
				return
			}

			// Set current job for tracking
			CrawlerEngine.StatsMutex.Lock()
			if CrawlerEngine.WorkerStats[WorkerID] != nil {
				CrawlerEngine.WorkerStats[WorkerID].CurrentJob = Job
			}
			CrawlerEngine.StatsMutex.Unlock()

			// 5. Process each job by calling ProcessJob method
			Result := CrawlerEngine.ProcessJob(Context, Job, WorkerID)

			// 6. Update worker statistics after each job
			CrawlerEngine.UpdateWorkerStats(WorkerID, Job, Result)

			// Send result to results channel (non-blocking with timeout)
			select {
			case CrawlerEngine.Results <- Result:
				// Successfully sent result
			case <-Context.Done():
				// Context canceled while sending result
				return
			case <-time.After(5 * time.Second):
				// Timeout sending result - log and continue
				CrawlerEngine.Logger.Warn("timeout sending result to results channel",
					zap.Int("worker_id", WorkerID),
					zap.String("url", Job.URL))
			}

			// Clear current job
			CrawlerEngine.StatsMutex.Lock()
			if CrawlerEngine.WorkerStats[WorkerID] != nil {
				CrawlerEngine.WorkerStats[WorkerID].CurrentJob = nil
			}
			CrawlerEngine.StatsMutex.Unlock()
		}
	}
}

// ProcessJob handles the actual crawling of a single URL
func (CrawlerEngine *CrawlerEngine) ProcessJob(Context context.Context, Job *CrawlJob, WorkerID int) *CrawlResult {
	// 1. Record job start time and update worker stats
	StartTime := time.Now()

	CrawlerEngine.Logger.Debug("processing job",
		zap.Int("worker_id", WorkerID),
		zap.String("url", Job.URL),
		zap.String("request_id", Job.RequestID))

	// Initialize result structure
	Result := &CrawlResult{
		Job:         Job,
		URL:         Job.URL,
		CompletedAt: time.Now(),
		Success:     false,
		Retryable:   false,
	}

	// 2. Check robots.txt permissions for the URL (already done in SubmitJob, but double-check)
	PermissionResult, RobotsErr := CrawlerEngine.RobotsParser.IsAllowed(Context, Job.URL)
	if RobotsErr != nil {
		CrawlerEngine.Logger.Warn("robots.txt check failed during processing",
			zap.String("url", Job.URL),
			zap.Error(RobotsErr))
		// Continue with crawling since we already checked in SubmitJob
	} else if !PermissionResult.Allowed {
		Result.Error = fmt.Errorf("URL disallowed by robots.txt: %s", Job.URL)
		Result.Retryable = false
		Result.ResponseTime = time.Since(StartTime)
		return Result
	}

	// 3. Apply rate limiting for the host (already done in SubmitJob for submission, but apply again for processing)
	HostFromURL, ExtractErr := frontier.ExtractHostFromURL(Job.URL)
	if ExtractErr != nil {
		Result.Error = fmt.Errorf("failed to extract host from URL: %w", ExtractErr)
		Result.Retryable = false
		Result.ResponseTime = time.Since(StartTime)
		return Result
	}

	HostLimiter := CrawlerEngine.GetRateLimiter(HostFromURL)
	if HostErr := HostLimiter.Wait(Context); HostErr != nil {
		Result.Error = fmt.Errorf("host rate limit wait failed: %w", HostErr)
		Result.Retryable = true // Rate limit errors are retryable
		Result.ResponseTime = time.Since(StartTime)
		return Result
	}

	// 4. Create HTTP request with appropriate headers
	HTTPResponse, HTTPErr := CrawlerEngine.HTTPClient.Get(Context, Job.URL)
	if HTTPErr != nil {
		Result.Error = fmt.Errorf("HTTP request failed: %w", HTTPErr)
		Result.Retryable = CrawlerEngine.IsRetryableHTTPError(HTTPErr)
		Result.ResponseTime = time.Since(StartTime)
		return Result
	}
	defer HTTPResponse.Body.Close()

	// 5. Execute HTTP request with timeout and context (handled above by HTTPClient.Get)

	// 6. Handle HTTP response and read body
	Result.StatusCode = HTTPResponse.StatusCode
	Result.Headers = HTTPResponse.Header
	Result.ContentType = HTTPResponse.Header.Get("Content-Type")
	Result.ContentLength = HTTPResponse.ContentLength
	Result.ResponseTime = time.Since(StartTime)

	// Read response body
	BodyBytes, ReadErr := io.ReadAll(HTTPResponse.Body)
	if ReadErr != nil {
		Result.Error = fmt.Errorf("failed to read response body: %w", ReadErr)
		Result.Retryable = true // Body read errors are typically retryable
		return Result
	}
	Result.Body = BodyBytes

	// Check for successful HTTP status
	if HTTPResponse.StatusCode < 200 || HTTPResponse.StatusCode >= 300 {
		Result.Error = fmt.Errorf("HTTP error status: %d", HTTPResponse.StatusCode)
		Result.Retryable = CrawlerEngine.IsRetryableHTTPStatus(HTTPResponse.StatusCode)
		return Result
	}

	// 7. Extract content using the content extractor factory
	ExtractedContent, ExtractionErr := CrawlerEngine.ExtractorFactory.ExtractContent(BodyBytes, Result.ContentType, Job.URL)
	if ExtractionErr != nil {
		CrawlerEngine.Logger.Warn("content extraction failed, continuing without extracted content",
			zap.String("url", Job.URL),
			zap.Error(ExtractionErr))

		// Continue without extracted content rather than failing the entire job
		Result.Links = []string{}
		Result.ExtractedText = ""
		Result.ExtractedContent = nil
	} else {
		// Populate result with extracted content
		Result.ExtractedContent = ExtractedContent
		Result.ExtractedText = ExtractedContent.CleanText

		// Convert ExtractedLink slice to string slice for backward compatibility
		ExtractedLinks := make([]string, len(ExtractedContent.Links))
		for i, link := range ExtractedContent.Links {
			ExtractedLinks[i] = link.URL
		}
		Result.Links = ExtractedLinks
	}

	// 8. Create CrawlResult with all collected data (already populated above)
	Result.Success = true
	Result.Retryable = false

	// Enhanced logging with extraction details
	logFields := []zap.Field{
		zap.Int("worker_id", WorkerID),
		zap.String("url", Job.URL),
		zap.Int("status_code", Result.StatusCode),
		zap.Duration("response_time", Result.ResponseTime),
		zap.Int("body_size", len(Result.Body)),
		zap.Int("extracted_links", len(Result.Links)),
	}

	if Result.ExtractedContent != nil {
		logFields = append(logFields,
			zap.String("title", Result.ExtractedContent.Title),
			zap.Int("word_count", Result.ExtractedContent.Metadata.WordCount),
			zap.Float64("quality_score", Result.ExtractedContent.QualityScore),
			zap.Int("text_length", len(Result.ExtractedContent.CleanText)))
	}

	CrawlerEngine.Logger.Debug("job processed successfully", logFields...)

	return Result
}

// ResultProcessor handles crawl results and manages output
func (CrawlerEngine *CrawlerEngine) ResultProcessor(Context context.Context) {
	// Decrement wait group when function exits
	defer CrawlerEngine.Wg.Done()

	CrawlerEngine.Logger.Info("result processor started")

	// 10. Log processor shutdown when loop exits
	defer func() {
		CrawlerEngine.Logger.Info("result processor stopped")
	}()

	// 1. Start result processing loop with context cancellation
	for {
		select {
		case <-Context.Done():
			// 9. Handle context cancellation and drain remaining results
			CrawlerEngine.Logger.Info("result processor received shutdown signal, draining remaining results")
			// Drain any remaining results with timeout
			DrainTimeout := time.After(10 * time.Second)
			for {
				select {
				case Result, ChannelOpen := <-CrawlerEngine.Results:
					if !ChannelOpen {
						CrawlerEngine.Logger.Info("results channel closed, result processor exiting")
						return
					}
					CrawlerEngine.ProcessResult(Result)
				case <-DrainTimeout:
					CrawlerEngine.Logger.Warn("timeout draining results, forcing shutdown")
					return
				}
			}

		case Result, ChannelOpen := <-CrawlerEngine.Results:
			// 2. Listen for results from results channel
			if !ChannelOpen {
				CrawlerEngine.Logger.Info("results channel closed, result processor exiting")
				return
			}

			CrawlerEngine.ProcessResult(Result)
		}
	}
}

// ProcessResult handles individual crawl results
func (CrawlerEngine *CrawlerEngine) ProcessResult(Result *CrawlResult) {
	// 3. Update global statistics (success/failure counters)
	if Result.Success {
		atomic.AddInt64(&CrawlerEngine.SuccessfulJobs, 1)
		// 4. Log result processing with URL and status
		CrawlerEngine.Logger.Debug("result processed successfully",
			zap.String("url", Result.URL),
			zap.Int("status_code", Result.StatusCode),
			zap.Duration("response_time", Result.ResponseTime),
			zap.Int("extracted_links", len(Result.Links)))

		// 5. Handle successful results (store data, extract links)
		// TODO: In the future, this would integrate with storage layer
		// For now, we just log the successful processing

	} else {
		atomic.AddInt64(&CrawlerEngine.FailedJobs, 1)
		// 6. Handle failed results (retry logic, error logging)
		CrawlerEngine.Logger.Warn("result processing failed",
			zap.String("url", Result.URL),
			zap.Int("status_code", Result.StatusCode),
			zap.Bool("retryable", Result.Retryable),
			zap.Error(Result.Error))

		// TODO: Implement retry logic here
		// If Result.Retryable is true, could resubmit job with exponential backoff
	}

	// 7. Send results to external processors if configured
	// TODO: This would be where we integrate with external result processors
	// such as storage systems, message queues, etc.

	// 8. Clean up resources associated with completed jobs
	// For now, results are handled by garbage collection
	// In a production system, we might need explicit cleanup
}

// MetricsCollector periodically collects and reports metrics
func (CrawlerEngine *CrawlerEngine) MetricsCollector(Context context.Context) {
	// Decrement wait group when function exits
	defer CrawlerEngine.Wg.Done()

	// 1. Create ticker for periodic metrics collection (default: 30s)
	Ticker := time.NewTicker(30 * time.Second)
	defer Ticker.Stop()

	CrawlerEngine.Logger.Info("metrics collector started")

	// 8. Log metrics collector shutdown
	defer func() {
		CrawlerEngine.Logger.Info("metrics collector stopped")
	}()

	// 2. Start metrics collection loop with context cancellation
	for {
		select {
		case <-Context.Done():
			// 6. Handle context cancellation gracefully
			CrawlerEngine.Logger.Debug("metrics collector received shutdown signal")
			// 7. Stop ticker and clean up resources (handled by defer)
			return

		case <-Ticker.C:
			// 3. On each tick, calculate current metrics using GetMetrics
			CurrentMetrics := CrawlerEngine.GetMetrics()
			if CurrentMetrics == nil {
				continue
			}

			// 4. Call metrics callback function if configured
			if CrawlerEngine.MetricsCallback != nil {
				go func(Metrics *CrawlerMetrics) {
					// Run callback in separate goroutine to avoid blocking
					defer func() {
						if RecoverErr := recover(); RecoverErr != nil {
							CrawlerEngine.Logger.Error("metrics callback panic",
								zap.Any("error", RecoverErr))
						}
					}()
					CrawlerEngine.MetricsCallback(Metrics)
				}(CurrentMetrics)
			}

			// 5. Log key metrics at INFO level for monitoring
			CrawlerEngine.Logger.Info("crawler metrics",
				zap.Int64("total_jobs", CurrentMetrics.TotalJobs),
				zap.Int64("successful_jobs", CurrentMetrics.SuccessfulJobs),
				zap.Int64("failed_jobs", CurrentMetrics.FailedJobs),
				zap.Float64("jobs_per_second", CurrentMetrics.JobsPerSecond),
				zap.Duration("average_latency", CurrentMetrics.AverageLatency),
				zap.Int("active_workers", CurrentMetrics.ActiveWorkers),
				zap.Int("queue_depth", CurrentMetrics.QueueDepth),
				zap.Duration("uptime", CurrentMetrics.Uptime),
				zap.Float64("error_rate", CurrentMetrics.ErrorRate))
		}
	}
}

// GetRateLimiter gets or creates a rate limiter for a specific host
func (CrawlerEngine *CrawlerEngine) GetRateLimiter(Host string) *rate.Limiter {
	// 1. Acquire read lock to check if limiter exists
	CrawlerEngine.LimiterMutex.RLock()
	if Limiter, exists := CrawlerEngine.HostLimiters[Host]; exists {
		CrawlerEngine.LimiterMutex.RUnlock()
		return Limiter
	}
	CrawlerEngine.LimiterMutex.RUnlock()

	// 3. Upgrade to write lock if limiter doesn't exist
	CrawlerEngine.LimiterMutex.Lock()
	defer CrawlerEngine.LimiterMutex.Unlock()

	// 4. Double-check pattern to avoid race condition
	if Limiter, exists := CrawlerEngine.HostLimiters[Host]; exists {
		return Limiter
	}

	// 5. Create new rate limiter with host-specific configuration
	// Use configurable per-host rate limits
	PerHostRateLimit := CrawlerEngine.Config.PerHostRateLimit
	if PerHostRateLimit <= 0 {
		PerHostRateLimit = 3.0 // Default 3 req/sec per host if not configured
	}
	PerHostBurst := CrawlerEngine.Config.PerHostBurst
	if PerHostBurst <= 0 {
		PerHostBurst = 5 // Default burst of 5 per host if not configured
	}

	// TODO: 8. Handle rate limit configuration from robots.txt crawl-delay
	// Check if we should respect crawl-delay from robots.txt
	// This would override the configured rate limit for this specific host
	HostLimiter := rate.NewLimiter(rate.Limit(PerHostRateLimit), PerHostBurst)

	// 6. Store limiter in map for future use
	CrawlerEngine.HostLimiters[Host] = HostLimiter

	CrawlerEngine.Logger.Debug("created new rate limiter for host",
		zap.String("host", Host),
		zap.Float64("rate_limit", float64(HostLimiter.Limit())),
		zap.Int("burst", HostLimiter.Burst()),
		zap.Float64("configured_per_host_rate", PerHostRateLimit),
		zap.Int("configured_per_host_burst", PerHostBurst))

	return HostLimiter
}

// IsRetryableHTTPError determines if an HTTP error should trigger a retry
func (CrawlerEngine *CrawlerEngine) IsRetryableHTTPError(Error error) bool {
	if Error == nil {
		return false
	}

	// Use the HTTP client's retry logic
	return client.IsRetryableError(Error, CrawlerEngine.HTTPClient.RetryConfig.RetryableErrors)
}

// IsRetryableHTTPStatus determines if an HTTP status code should trigger a retry
func (CrawlerEngine *CrawlerEngine) IsRetryableHTTPStatus(StatusCode int) bool {
	// Use the HTTP client's retry logic
	return client.IsRetryableStatus(StatusCode, CrawlerEngine.HTTPClient.RetryConfig.RetryableStatus)
}

// cleanupRateLimiters removes unused rate limiters to prevent memory leaks
func (CrawlerEngine *CrawlerEngine) cleanupRateLimiters(Context context.Context) {
	// Decrement wait group when function exits
	defer CrawlerEngine.Wg.Done()

	// 1. Create ticker for periodic cleanup (default: 10 minutes)
	Ticker := time.NewTicker(10 * time.Minute)
	defer Ticker.Stop()

	CrawlerEngine.Logger.Info("starting rate limiter cleanup goroutine", zap.Duration("interval", 10*time.Minute))

	// 2. Start cleanup loop with context cancellation
	for {
		select {
		case <-Context.Done():
			// 6. Handle context cancellation gracefully
			CrawlerEngine.Logger.Info("rate limiter cleanup goroutine stopping due to context cancellation")
			return

		case <-Ticker.C:
			// 3. On each tick, iterate through rate limiters
			CrawlerEngine.LimiterMutex.Lock()

			InitialCount := len(CrawlerEngine.HostLimiters)
			RemovedCount := 0

			// 4. Remove limiters that haven't been used recently
			// Simple approach: remove limiters with no recent activity
			// More sophisticated: track last use time and remove old ones
			for Host, Limiter := range CrawlerEngine.HostLimiters {
				// Check if limiter has available tokens (indicating no recent heavy use)
				// This is a simple heuristic - if burst is fully available, likely unused
				if Limiter.Tokens() >= float64(Limiter.Burst()) {
					delete(CrawlerEngine.HostLimiters, Host)
					RemovedCount++
				}
			}

			CrawlerEngine.LimiterMutex.Unlock()

			// 5. Log cleanup statistics (removed/remaining limiters)
			if RemovedCount > 0 {
				CrawlerEngine.Logger.Info("cleaned up unused rate limiters",
					zap.Int("removed", RemovedCount),
					zap.Int("remaining", InitialCount-RemovedCount),
					zap.Int("initial", InitialCount))
			}
		}
	}
}

// IsRunning safely checks if the crawler engine is running
func (CrawlerEngine *CrawlerEngine) IsRunning() bool {
	// 1. Acquire read lock for thread safety
	CrawlerEngine.RunningMutex.RLock()
	defer CrawlerEngine.RunningMutex.RUnlock()

	// 2. Read running status
	// 3. Release lock and return status (handled by defer)
	return CrawlerEngine.Running
}

// UpdateWorkerStats safely updates statistics for a worker
func (CrawlerEngine *CrawlerEngine) UpdateWorkerStats(WorkerID int, Job *CrawlJob, Result *CrawlResult) {
	// 1. Acquire write lock for thread safety
	CrawlerEngine.StatsMutex.Lock()
	defer CrawlerEngine.StatsMutex.Unlock()

	// 2. Get or create worker stats for WorkerID
	CurrentWorkerStats, exists := CrawlerEngine.WorkerStats[WorkerID]
	if !exists {
		// This shouldn't happen if engine is properly initialized, but handle gracefully
		CurrentWorkerStats = &WorkerStats{
			WorkerID:       WorkerID,
			JobsProcessed:  0,
			JobsSuccessful: 0,
			JobsFailed:     0,
			TotalTime:      0,
			AverageTime:    0,
			LastJobTime:    time.Time{},
			IsActive:       false,
			CurrentJob:     nil,
		}
		CrawlerEngine.WorkerStats[WorkerID] = CurrentWorkerStats
	}

	// 3. Increment job counters based on result success/failure
	CurrentWorkerStats.JobsProcessed++
	if Result.Success {
		CurrentWorkerStats.JobsSuccessful++
	} else {
		CurrentWorkerStats.JobsFailed++
	}

	// 4. Update timing statistics with job duration
	JobDuration := Result.ResponseTime
	if JobDuration > 0 {
		CurrentWorkerStats.TotalTime += JobDuration

		// 5. Calculate running average of job processing time
		CurrentWorkerStats.AverageTime = CurrentWorkerStats.TotalTime / time.Duration(CurrentWorkerStats.JobsProcessed)
	}

	// 6. Update last job time and current job reference
	CurrentWorkerStats.LastJobTime = time.Now()
	CurrentWorkerStats.CurrentJob = nil // Job is completed

	// 7. Release lock after updates complete (handled by defer)
}
