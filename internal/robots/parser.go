// Package robots provides robots.txt fetching, caching, and permission checking functionality
// for ethical web crawling according to the Robots Exclusion Standard.
package robots

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/temoto/robotstxt"
)

// CacheEntry represents a cached robots.txt entry with expiration
type CacheEntry struct {
	Robots    *robotstxt.RobotsData
	FetchedAt time.Time
	ExpiresAt time.Time
}

// Parser handles robots.txt fetching, caching, and permission checking
type Parser struct {
	client     *http.Client
	cache      map[string]*CacheEntry
	cacheMutex sync.RWMutex
	cacheTTL   time.Duration // Time To Live
	userAgent  string
	fetchMutex map[string]*sync.Mutex // Per-host fetch mutex to prevent concurrent requests
	mutexLock  sync.Mutex             // Protects fetchMutex map
}

// Config holds configuration for the robots.txt parser
type Config struct {
	UserAgent   string
	CacheTTL    time.Duration // Time To Live (default: 24hrs)
	HTTPTimeout time.Duration
	MaxSize     int64 // (default: 500KB)
}

// PermissionResult represents the result of a permission check
type PermissionResult struct {
	Allowed      bool
	CrawlDelay   time.Duration
	DisallowedBy string   // Rule that disallowed the URL (empty if allowed)
	Sitemaps     []string // Sitemap URLs found in robots.txt
}

// FetchResult represents the result of fetching robots.txt
type FetchResult struct {
	Success      bool
	Robots       *robotstxt.RobotsData
	Error        error
	ResponseCode int
	FetchTime    time.Duration
}

// NewParser creates a new robots.txt parser with the given configuration
func NewParser(config Config) *Parser {
	if config.UserAgent == "" {
		config.UserAgent = "*"
	}
	if config.CacheTTL <= 0 {
		config.CacheTTL = 24 * time.Hour
	}
	if config.HTTPTimeout <= 0 {
		config.HTTPTimeout = 30 * time.Second
	}
	if config.MaxSize <= 0 {
		config.MaxSize = 500 * 1024 // 500KB
	}

	client := &http.Client{
		Timeout: config.HTTPTimeout,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	return &Parser{
		client:     client,
		cache:      make(map[string]*CacheEntry),
		cacheMutex: sync.RWMutex{},
		cacheTTL:   config.CacheTTL,
		userAgent:  config.UserAgent,
		fetchMutex: make(map[string]*sync.Mutex),
		mutexLock:  sync.Mutex{},
	}
}

// IsAllowed checks if a URL is allowed to be crawled according to robots.txt
// This is the main function that crawlers will use
func (p *Parser) IsAllowed(ctx context.Context, urlStr string) (*PermissionResult, error) {
	if urlStr == "" {
		return nil, fmt.Errorf("empty URL provided")
	}
	host, err := extractHostFromURL(urlStr)
	if err != nil {
		return nil, fmt.Errorf("failed to extract host from URL %q: %w", urlStr, err)
	}
	robots, err := p.GetRobots(ctx, host)
	if err != nil {
		// If we can't fetch robots.txt, be permissive and allow crawling
		return &PermissionResult{
			Allowed:      true,
			CrawlDelay:   0,
			DisallowedBy: "",
			Sitemaps:     []string{},
		}, nil
	}
	allowed := robots.TestAgent(urlStr, p.userAgent)
	var crawlDelay time.Duration
	if group := robots.FindGroup(p.userAgent); group != nil {
		// CrawlDelay in robotstxt library is time.Duration in nanoseconds
		// Convert back to seconds by dividing by time.Second, then multiply back to get clean seconds
		if group.CrawlDelay > 0 {
			seconds := int64(group.CrawlDelay / time.Second)
			crawlDelay = time.Duration(seconds) * time.Second
		}
	}
	var sitemaps []string
	if robots.Sitemaps != nil {
		sitemaps = robots.Sitemaps
	} else {
		sitemaps = []string{}
	}
	disallowedBy := ""
	if !allowed {
		disallowedBy = "robots.txt disallow rule"
	}

	return &PermissionResult{
		Allowed:      allowed,
		CrawlDelay:   crawlDelay,
		DisallowedBy: disallowedBy,
		Sitemaps:     sitemaps,
	}, nil
}

// GetRobots fetches and returns robots.txt for a given host
// Uses caching with 24-hour expiration
func (p *Parser) GetRobots(ctx context.Context, host string) (*robotstxt.RobotsData, error) {
	if host == "" {
		return nil, fmt.Errorf("empty host provided")
	}
	normalizedHost := host
	if strings.Contains(host, ":") {
		if h, _, err := net.SplitHostPort(host); err == nil {
			normalizedHost = h
		}
	}
	if robots, found := p.getCachedRobots(normalizedHost); found {
		return robots, nil
	}
	hostMutex := p.getPerHostMutex(normalizedHost)
	hostMutex.Lock()
	defer hostMutex.Unlock()

	// Check cache again in case another goroutine fetched it while we waited
	if robots, found := p.getCachedRobots(normalizedHost); found {
		return robots, nil
	}
	fetchResult, err := p.fetchRobotsFromServer(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch robots.txt for host %q: %w", host, err)
	}

	if !fetchResult.Success || fetchResult.Robots == nil {
		// Create permissive robots.txt for failed fetches
		permissiveRobots, _ := robotstxt.FromString("")
		p.setCachedRobots(normalizedHost, permissiveRobots)
		return permissiveRobots, nil
	}
	p.setCachedRobots(normalizedHost, fetchResult.Robots)
	return fetchResult.Robots, nil
}

// fetchRobotsFromServer fetches robots.txt from the server for a given host
func (p *Parser) fetchRobotsFromServer(ctx context.Context, host string) (*FetchResult, error) {
	start := time.Now()
	robotsURL := buildRobotsURL(host)
	if robotsURL == "" {
		return &FetchResult{
			Success:      false,
			Robots:       nil,
			Error:        fmt.Errorf("failed to build robots.txt URL for host %q", host),
			ResponseCode: 0,
			FetchTime:    time.Since(start),
		}, nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", robotsURL, nil)
	if err != nil {
		return &FetchResult{
			Success:      false,
			Robots:       nil,
			Error:        fmt.Errorf("failed to create request: %w", err),
			ResponseCode: 0,
			FetchTime:    time.Since(start),
		}, nil
	}
	req.Header.Set("User-Agent", fmt.Sprintf("Mozilla/5.0 (compatible; %s; +https://github.com/Almahr1/quert)", p.userAgent))
	req.Header.Set("Accept", "text/plain")
	resp, err := p.client.Do(req)
	if err != nil {
		return &FetchResult{
			Success:      false,
			Robots:       nil,
			Error:        fmt.Errorf("HTTP request failed: %w", err),
			ResponseCode: 0,
			FetchTime:    time.Since(start),
		}, nil
	}
	defer resp.Body.Close()
	result := &FetchResult{
		Success:      false,
		Robots:       nil,
		Error:        nil,
		ResponseCode: resp.StatusCode,
		FetchTime:    time.Since(start),
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusForbidden, http.StatusUnauthorized:
		permissiveRobots, _ := robotstxt.FromString("")
		result.Success = true
		result.Robots = permissiveRobots
		return result, nil
	default:
		result.Error = fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		return result, nil
	}
	limitedReader := io.LimitReader(resp.Body, 500*1024) // 500KB limit
	content, err := io.ReadAll(limitedReader)
	if err != nil {
		result.Error = fmt.Errorf("failed to read response body: %w", err)
		return result, nil
	}
	if len(content) >= 500*1024 {
		result.Error = fmt.Errorf("robots.txt too large (>500KB)")
		return result, nil
	}
	robots, err := parseRobotsContent(content)
	if err != nil {
		result.Error = fmt.Errorf("failed to parse robots.txt: %w", err)
		return result, nil
	}
	result.Success = true
	result.Robots = robots
	return result, nil
}

// getPerHostMutex returns a mutex for the given host to prevent concurrent fetching
func (p *Parser) getPerHostMutex(host string) *sync.Mutex {
	p.mutexLock.Lock()
	defer p.mutexLock.Unlock()
	if mutex, exists := p.fetchMutex[host]; exists {
		return mutex
	}
	mutex := &sync.Mutex{}
	p.fetchMutex[host] = mutex
	return mutex
}

// isExpired checks if a cache entry has expired
func (p *Parser) isExpired(entry *CacheEntry) bool {
	if entry == nil {
		return true
	}
	return time.Now().After(entry.ExpiresAt)
}

// getCachedRobots returns cached robots.txt if it exists and is not expired
func (p *Parser) getCachedRobots(host string) (*robotstxt.RobotsData, bool) {
	p.cacheMutex.RLock()
	defer p.cacheMutex.RUnlock()
	entry, exists := p.cache[host]
	if !exists {
		return nil, false
	}
	if p.isExpired(entry) {
		return nil, false
	}
	return entry.Robots, true
}

// setCachedRobots stores robots.txt in the cache with expiration
func (p *Parser) setCachedRobots(host string, robots *robotstxt.RobotsData) {
	p.cacheMutex.Lock()
	defer p.cacheMutex.Unlock()
	now := time.Now()
	entry := &CacheEntry{
		Robots:    robots,
		FetchedAt: now,
		ExpiresAt: now.Add(p.cacheTTL),
	}
	p.cache[host] = entry
}

// ClearCache removes all entries from the robots.txt cache
func (p *Parser) ClearCache() {
	p.cacheMutex.Lock()
	defer p.cacheMutex.Unlock()
	p.cache = make(map[string]*CacheEntry)
}

// ClearExpired removes expired entries from the cache
func (p *Parser) ClearExpired() int {
	p.cacheMutex.Lock()
	defer p.cacheMutex.Unlock()
	removedCount := 0
	for host, entry := range p.cache {
		if p.isExpired(entry) {
			delete(p.cache, host)
			removedCount++
		}
	}
	return removedCount
}

// GetCacheStats returns statistics about the robots.txt cache
func (p *Parser) GetCacheStats() CacheStats {
	p.cacheMutex.RLock()
	defer p.cacheMutex.RUnlock()
	totalEntries := len(p.cache)
	expiredEntries := 0
	var oldestEntry, newestEntry time.Time
	var totalAge time.Duration

	if totalEntries == 0 {
		return CacheStats{
			TotalEntries:   0,
			ExpiredEntries: 0,
			OldestEntry:    time.Time{},
			NewestEntry:    time.Time{},
			CacheHitRate:   0.0,
			AverageAge:     0,
		}
	}
	first := true
	for _, entry := range p.cache {
		if p.isExpired(entry) {
			expiredEntries++
		}

		fetchTime := entry.FetchedAt
		if first {
			oldestEntry = fetchTime
			newestEntry = fetchTime
			first = false
		} else {
			if fetchTime.Before(oldestEntry) {
				oldestEntry = fetchTime
			}
			if fetchTime.After(newestEntry) {
				newestEntry = fetchTime
			}
		}

		totalAge += time.Since(fetchTime)
	}

	averageAge := time.Duration(0)
	if totalEntries > 0 {
		averageAge = totalAge / time.Duration(totalEntries)
	}
	return CacheStats{
		TotalEntries:   totalEntries,
		ExpiredEntries: expiredEntries,
		OldestEntry:    oldestEntry,
		NewestEntry:    newestEntry,
		CacheHitRate:   0.0, // Not tracking hits/misses yet
		AverageAge:     averageAge,
	}
}

// CacheStats holds statistics about the robots.txt cache
type CacheStats struct {
	TotalEntries   int           // Total number of cached entries
	ExpiredEntries int           // Number of expired entries
	OldestEntry    time.Time     // Timestamp of oldest entry
	NewestEntry    time.Time     // Timestamp of newest entry
	CacheHitRate   float64       // Cache hit rate (if tracking)
	AverageAge     time.Duration // Average age of cache entries
}

// extractHostFromURL extracts the host (domain) from a URL string
func extractHostFromURL(urlStr string) (string, error) {
	if !strings.Contains(urlStr, "://") {
		urlStr = "http://" + urlStr
	}

	u, err := url.Parse(urlStr)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}

	host := u.Host
	if host == "" {
		return "", fmt.Errorf("no host found in URL")
	}

	return host, nil
}

// buildRobotsURL constructs the robots.txt URL for a given host
func buildRobotsURL(host string) string {
	host = strings.TrimSpace(host)

	if host == "" {
		return ""
	}

	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "http://" + host
	}

	u, err := url.Parse(host)
	if err != nil {
			if !strings.HasSuffix(host, "/") {
			host += "/"
		}
		return host + "robots.txt"
	}
	u.Path = "/robots.txt"
	u.RawQuery = ""
	u.Fragment = ""

	return u.String()

}

// parseRobotsContent parses robots.txt content and returns RobotsData
func parseRobotsContent(content []byte) (*robotstxt.RobotsData, error) {
	if len(content) == 0 {
		r, _ := robotstxt.FromString("") // ignore error, empty string is valid
		return r, nil
	}
	r, err := robotstxt.FromBytes(content)
	if err != nil {
		r, _ = robotstxt.FromString("")
		return r, fmt.Errorf("failed to parse robots.txt, returning permissive: %w", err)
	}

	if r == nil {
		r, _ = robotstxt.FromString("")
		return r, fmt.Errorf("robots.txt parsing returned nil data, returning permissive")
	}

	return r, nil
}

// Close gracefully shuts down the parser and cleans up resources
func (p *Parser) Close() error {
	p.ClearCache()
	if transport, ok := p.client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
	p.mutexLock.Lock()
	p.fetchMutex = make(map[string]*sync.Mutex)
	p.mutexLock.Unlock()

	return nil
}
