package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Almahr1/quert/internal/config"
	"go.uber.org/zap"
)

type HTTPClient struct {
	Client      *http.Client
	Config      *config.HTTPConfig
	Logger      *zap.Logger
	Middleware  []Middleware
	RetryConfig RetryConfig
}

type RetryConfig struct {
	MaxRetries      int
	BackoffStrategy BackoffStrategy
	RetryableErrors []error
	RetryableStatus []int
}

type BackoffStrategy interface {
	NextDelay(attempt int) time.Duration
}

type ExponentialBackoff struct {
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	Multiplier float64
	Jitter     bool
}

type LinearBackoff struct {
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

type Middleware interface {
	RoundTrip(req *http.Request, next http.RoundTripper) (*http.Response, error)
}

type LoggingMiddleware struct {
	Logger *zap.Logger
}

type UserAgentMiddleware struct {
	UserAgent string
}

type TimeoutMiddleware struct {
	Timeout time.Duration
}

type RateLimitMiddleware struct {
	// TODO: Add rate limiter fields
}

type MetricsMiddleware struct {
	// TODO: Add metrics collection fields
}

type Response struct {
	*http.Response
	URL           string
	StatusCode    int
	ContentLength int64
	Duration      time.Duration
	Attempts      int
}

func NewHTTPClient(cfg *config.HTTPConfig, logger *zap.Logger) *HTTPClient {
	client := BuildHTTPClient(cfg)
	retryConfig := RetryConfig{
		MaxRetries: 3,
		BackoffStrategy: &ExponentialBackoff{
			BaseDelay:  500 * time.Millisecond,
			MaxDelay:   30 * time.Second,
			Multiplier: 2.0,
			Jitter:     true,
		},
		RetryableStatus: []int{429, 500, 502, 503, 504},
		RetryableErrors: []error{},
	}
	middleware := []Middleware{
		NewLoggingMiddleware(logger),
		NewUserAgentMiddleware("WebCrawler/1.0"),
	}

	return &HTTPClient{
		Client:      client,
		Config:      cfg,
		Logger:      logger,
		Middleware:  middleware,
		RetryConfig: retryConfig,
	}
}

func NewHTTPClientWithMiddleware(cfg *config.HTTPConfig, logger *zap.Logger, middleware ...Middleware) *HTTPClient {
	client := NewHTTPClient(cfg, logger)
	client.Middleware = append(client.Middleware, middleware...)
	return client
}

func (c *HTTPClient) Get(ctx context.Context, url string) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("GET Request Failed: %w", err)
	}
	return c.Do(ctx, req)
}

func (c *HTTPClient) Post(ctx context.Context, url string, contentType string, body interface{}) (*Response, error) {
	var reqBody io.Reader

	if body != nil {
		switch v := body.(type) {
		case string:
			reqBody = strings.NewReader(v)
		case []byte:
			reqBody = bytes.NewReader(v)
		case io.Reader:
			reqBody = v
		default:
			// Assume it's a struct to be JSON marshaled
			jsonData, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal request body: %w", err)
			}
			reqBody = bytes.NewReader(jsonData)
			if contentType == "" {
				contentType = "application/json"
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("POST request creation failed: %w", err)
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	return c.Do(ctx, req)
}

func (c *HTTPClient) Head(ctx context.Context, url string) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return nil, fmt.Errorf("HEAD request creation failed: %w", err)
	}
	return c.Do(ctx, req)
}

func (c *HTTPClient) Do(ctx context.Context, req *http.Request) (*Response, error) {
	start := time.Now()
	var lastErr error
	var resp *http.Response

	transport := ChainMiddleware(c.Middleware, c.Client.Transport)

	for attempt := 0; attempt <= c.RetryConfig.MaxRetries; attempt++ {
		reqClone := req.Clone(ctx)

		if attempt > 0 {
			delay := c.RetryConfig.BackoffStrategy.NextDelay(attempt)
			c.Logger.Debug("retrying request",
				zap.String("url", req.URL.String()),
				zap.Int("attempt", attempt),
				zap.Duration("delay", delay),
			)

			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		resp, lastErr = transport.RoundTrip(reqClone)

		if lastErr == nil {
			if !IsRetryableStatus(resp.StatusCode, c.RetryConfig.RetryableStatus) {
				break
			}
			resp.Body.Close()
			lastErr = fmt.Errorf("received retryable status code: %d", resp.StatusCode)
			continue
		}

		if !IsRetryableError(lastErr, c.RetryConfig.RetryableErrors) {
			break
		}

		c.Logger.Warn("request failed, will retry",
			zap.String("url", req.URL.String()),
			zap.Int("attempt", attempt),
			zap.Error(lastErr),
		)
	}

	if lastErr != nil {
		return nil, fmt.Errorf("request failed after %d attempts: %w", c.RetryConfig.MaxRetries+1, lastErr)
	}

	duration := time.Since(start)
	response := &Response{
		Response:      resp,
		URL:           req.URL.String(),
		StatusCode:    resp.StatusCode,
		ContentLength: resp.ContentLength,
		Duration:      duration,
		Attempts:      1, // Will be updated in retry logic if needed
	}

	return response, nil
}

func (c *HTTPClient) AddMiddleware(middleware ...Middleware) {
	c.Middleware = append(c.Middleware, middleware...)
}

func (c *HTTPClient) SetRetryConfig(config RetryConfig) {
	c.RetryConfig = config
}

func (c *HTTPClient) Close() error {
	c.Client.CloseIdleConnections()
	c.Logger.Info("HTTP client closed")
	return nil
}

func (e *ExponentialBackoff) NextDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}

	delay := float64(e.BaseDelay) * math.Pow(e.Multiplier, float64(attempt-1))

	if e.Jitter {
		jitter := delay * 0.1 * rand.Float64()
		delay += jitter
	}

	result := time.Duration(delay)
	if result > e.MaxDelay {
		result = e.MaxDelay
	}

	return result
}

func (l *LinearBackoff) NextDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}

	delay := time.Duration(attempt) * l.BaseDelay

	if delay > l.MaxDelay {
		delay = l.MaxDelay
	}

	return delay
}

func (m *LoggingMiddleware) RoundTrip(req *http.Request, next http.RoundTripper) (*http.Response, error) {
	start := time.Now()

	m.Logger.Debug("HTTP request",
		zap.String("method", req.Method),
		zap.String("url", req.URL.String()),
		zap.String("user_agent", req.Header.Get("User-Agent")),
	)

	resp, err := next.RoundTrip(req)
	duration := time.Since(start)

	if err != nil {
		m.Logger.Error("HTTP request failed",
			zap.String("method", req.Method),
			zap.String("url", req.URL.String()),
			zap.Duration("duration", duration),
			zap.Error(err),
		)
	} else {
		m.Logger.Debug("HTTP response",
			zap.String("method", req.Method),
			zap.String("url", req.URL.String()),
			zap.Int("status", resp.StatusCode),
			zap.Int64("content_length", resp.ContentLength),
			zap.Duration("duration", duration),
		)
	}

	return resp, err
}

func (m *UserAgentMiddleware) RoundTrip(req *http.Request, next http.RoundTripper) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", m.UserAgent)
	}
	return next.RoundTrip(req)
}

func (m *TimeoutMiddleware) RoundTrip(req *http.Request, next http.RoundTripper) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(req.Context(), m.Timeout)
	defer cancel()

	reqWithTimeout := req.WithContext(ctx)
	return next.RoundTrip(reqWithTimeout)
}

func (m *RateLimitMiddleware) RoundTrip(req *http.Request, next http.RoundTripper) (*http.Response, error) {
	// TODO: For now, just pass through. A full implementation would need
	// a rate limiter like golang.org/x/time/rate or similar
	// This would typically include per-host rate limiting for web crawling
	return next.RoundTrip(req)
}

func (m *MetricsMiddleware) RoundTrip(req *http.Request, next http.RoundTripper) (*http.Response, error) {
	start := time.Now()

	resp, err := next.RoundTrip(req)
	duration := time.Since(start)

	// TODO: Record metrics here. In a full implementation, you'd update
	// Prometheus counters, histograms, etc. For example:
	// - HTTP request total counter by method and status
	// - Request duration histogram using duration variable
	// - Response size histogram
	// - Error rate by type

	_ = duration // Suppress unused variable warning until metrics are implemented

	return resp, err
}

func NewLoggingMiddleware(logger *zap.Logger) *LoggingMiddleware {
	return &LoggingMiddleware{
		Logger: logger,
	}
}

func NewUserAgentMiddleware(userAgent string) *UserAgentMiddleware {
	return &UserAgentMiddleware{
		UserAgent: userAgent,
	}
}

func NewTimeoutMiddleware(timeout time.Duration) *TimeoutMiddleware {
	return &TimeoutMiddleware{
		Timeout: timeout,
	}
}

func NewRateLimitMiddleware() *RateLimitMiddleware {
	return &RateLimitMiddleware{
		// TODO: Initialize rate limiter fields
	}
}

func NewMetricsMiddleware() *MetricsMiddleware {
	return &MetricsMiddleware{
		// TODO: Initialize metrics collector fields
	}
}

func IsRetryableError(err error, retryableErrors []error) bool {
	if err == nil {
		return false
	}

	// Check against explicitly configured retryable errors
	for _, retryableErr := range retryableErrors {
		if err == retryableErr {
			return true
		}
	}

	// Check for common network errors that should trigger retry
	errorStr := err.Error()

	// Network timeout errors
	if strings.Contains(errorStr, "timeout") {
		return true
	}

	// DNS errors (temporary)
	if strings.Contains(errorStr, "no such host") {
		return true
	}

	// Connection refused (server might be temporarily down)
	if strings.Contains(errorStr, "connection refused") {
		return true
	}

	// Connection reset by peer
	if strings.Contains(errorStr, "connection reset by peer") {
		return true
	}

	// EOF errors (connection closed unexpectedly)
	if strings.Contains(errorStr, "EOF") {
		return true
	}

	// Context canceled or deadline exceeded (from timeouts)
	if strings.Contains(errorStr, "context canceled") || strings.Contains(errorStr, "deadline exceeded") {
		return true
	}

	return false
}

// Pre-built map of default retryable status codes for efficiency
var defaultRetryableStatusCodes = map[int]struct{}{
	429: {}, // Too Many Requests
	500: {}, // Internal Server Error
	502: {}, // Bad Gateway
	503: {}, // Service Unavailable
	504: {}, // Gateway Timeout
}

func IsRetryableStatus(statusCode int, retryableStatus []int) bool {
	// Use pre-built map if using default retryable status codes
	if len(retryableStatus) == 5 {
		isDefault := true
		for _, code := range retryableStatus {
			if _, exists := defaultRetryableStatusCodes[code]; !exists {
				isDefault = false
				break
			}
		}
		if isDefault {
			_, exists := defaultRetryableStatusCodes[statusCode]
			return exists
		}
	}

	// Fallback to creating map for custom retryable status codes
	set := make(map[int]struct{}, len(retryableStatus))
	for _, code := range retryableStatus {
		set[code] = struct{}{}
	}

	_, exists := set[statusCode]
	return exists
}

func BuildHTTPClient(cfg *config.HTTPConfig) *http.Client {
	dialer := &net.Dialer{
		Timeout: cfg.DialTimeout,
	}

	transport := &http.Transport{
		MaxIdleConns:          cfg.MaxIdleConnections,
		MaxIdleConnsPerHost:   cfg.MaxIdleConnectionsPerHost,
		IdleConnTimeout:       cfg.IdleConnectionTimeout,
		DisableKeepAlives:     cfg.DisableKeepAlives,
		DisableCompression:    cfg.DisableCompression,
		TLSHandshakeTimeout:   cfg.TlsHandshakeTimeout,
		ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,
		DialContext:           dialer.DialContext,
	}

	client := &http.Client{
		Timeout:   cfg.Timeout,
		Transport: transport,
	}

	return client
}

func ChainMiddleware(middleware []Middleware, base http.RoundTripper) http.RoundTripper {
	if len(middleware) == 0 {
		return base
	}

	// Create a middleware chain by wrapping each middleware around the next
	// We build from the inside out (base -> middleware[n-1] -> ... -> middleware[0])
	result := &middlewareChain{
		Middleware: middleware[len(middleware)-1],
		Next:       base,
	}

	// Wrap each middleware around the previous result
	for i := len(middleware) - 2; i >= 0; i-- {
		result = &middlewareChain{
			Middleware: middleware[i],
			Next:       result,
		}
	}

	return result
}

type middlewareChain struct {
	Middleware Middleware
	Next       http.RoundTripper
}

func (m *middlewareChain) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.Middleware.RoundTrip(req, m.Next)
}
