// Package robots provides robots.txt fetching, caching, and permission checking functionality
// for ethical web crawling according to the Robots Exclusion Standard.
package robots

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Almahr1/quert/internal/client"
	"github.com/Almahr1/quert/internal/frontier"
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
	HTTPClient *client.HTTPClient
	Cache      map[string]*CacheEntry
	CacheMutex sync.RWMutex
	CacheTTL   time.Duration // Time To Live
	UserAgent  string
	FetchMutex map[string]*sync.Mutex // Per-host fetch mutex to prevent concurrent requests
	MutexLock  sync.Mutex             // Protects fetchMutex map
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

// NewParser creates a new robots.txt parser with the given configuration and HTTP client
func NewParser(Config Config, HTTPClient *client.HTTPClient) *Parser {
	if Config.UserAgent == "" {
		Config.UserAgent = "*"
	}
	if Config.CacheTTL <= 0 {
		Config.CacheTTL = 24 * time.Hour
	}
	if Config.HTTPTimeout <= 0 {
		Config.HTTPTimeout = 30 * time.Second
	}
	if Config.MaxSize <= 0 {
		Config.MaxSize = 500 * 1024 // 500KB
	}

	return &Parser{
		HTTPClient: HTTPClient,
		Cache:      make(map[string]*CacheEntry),
		CacheMutex: sync.RWMutex{},
		CacheTTL:   Config.CacheTTL,
		UserAgent:  Config.UserAgent,
		FetchMutex: make(map[string]*sync.Mutex),
		MutexLock:  sync.Mutex{},
	}
}

// IsAllowed checks if a URL is allowed to be crawled according to robots.txt
// This is the main function that crawlers will use
func (P *Parser) IsAllowed(Ctx context.Context, RawURL string) (*PermissionResult, error) {
	if RawURL == "" {
		return nil, fmt.Errorf("empty URL provided")
	}
	Host, Err := frontier.ExtractHostFromURL(RawURL)
	if Err != nil {
		return nil, fmt.Errorf("failed to extract host from URL %q: %w", RawURL, Err)
	}
	Robots, Err := P.GetRobots(Ctx, Host)
	if Err != nil {
		// If we can't fetch robots.txt, be permissive and allow crawling
		return &PermissionResult{
			Allowed:      true,
			CrawlDelay:   0,
			DisallowedBy: "",
			Sitemaps:     []string{},
		}, nil
	}
	Allowed := Robots.TestAgent(RawURL, P.UserAgent)
	var CrawlDelay time.Duration
	if Group := Robots.FindGroup(P.UserAgent); Group != nil {
		// CrawlDelay in robotstxt library is time.Duration in nanoseconds
		// Convert back to seconds by dividing by time.Second, then multiply back to get clean seconds
		if Group.CrawlDelay > 0 {
			Seconds := int64(Group.CrawlDelay / time.Second)
			CrawlDelay = time.Duration(Seconds) * time.Second
		}
	}
	var Sitemaps []string
	if Robots.Sitemaps != nil {
		Sitemaps = Robots.Sitemaps
	} else {
		Sitemaps = []string{}
	}
	DisallowedBy := ""
	if !Allowed {
		DisallowedBy = "robots.txt disallow rule"
	}

	return &PermissionResult{
		Allowed:      Allowed,
		CrawlDelay:   CrawlDelay,
		DisallowedBy: DisallowedBy,
		Sitemaps:     Sitemaps,
	}, nil
}

// GetRobots fetches and returns robots.txt for a given host
// Uses caching with 24-hour expiration
func (P *Parser) GetRobots(Ctx context.Context, Host string) (*robotstxt.RobotsData, error) {
	if Host == "" {
		return nil, fmt.Errorf("empty host provided")
	}
	NormalizedHost := Host
	if strings.Contains(Host, ":") {
		if H, _, Err := net.SplitHostPort(Host); Err == nil {
			NormalizedHost = H
		}
	}
	if Robots, Found := P.GetCachedRobots(NormalizedHost); Found {
		return Robots, nil
	}
	HostMutex := P.GetPerHostMutex(NormalizedHost)
	HostMutex.Lock()
	defer HostMutex.Unlock()

	// Check cache again in case another goroutine fetched it while we waited
	if Robots, Found := P.GetCachedRobots(NormalizedHost); Found {
		return Robots, nil
	}
	FetchResult, Err := P.FetchRobotsFromServer(Ctx, Host)
	if Err != nil {
		return nil, fmt.Errorf("failed to fetch robots.txt for host %q: %w", Host, Err)
	}

	if !FetchResult.Success || FetchResult.Robots == nil {
		// Create permissive robots.txt for failed fetches
		PermissiveRobots, _ := robotstxt.FromString("")
		P.SetCachedRobots(NormalizedHost, PermissiveRobots)
		return PermissiveRobots, nil
	}
	P.SetCachedRobots(NormalizedHost, FetchResult.Robots)
	return FetchResult.Robots, nil
}

// FetchRobotsFromServer fetches robots.txt from the server for a given host
func (P *Parser) FetchRobotsFromServer(Ctx context.Context, Host string) (*FetchResult, error) {
	Start := time.Now()
	RobotsURL := BuildRobotsURL(Host)
	if RobotsURL == "" {
		return &FetchResult{
			Success:      false,
			Robots:       nil,
			Error:        fmt.Errorf("failed to build robots.txt URL for host %q", Host),
			ResponseCode: 0,
			FetchTime:    time.Since(Start),
		}, nil
	}

	// Use the centralized HTTP client
	Resp, Err := P.HTTPClient.Get(Ctx, RobotsURL)
	if Err != nil {
		return &FetchResult{
			Success:      false,
			Robots:       nil,
			Error:        fmt.Errorf("HTTP request failed: %w", Err),
			ResponseCode: 0,
			FetchTime:    time.Since(Start),
		}, nil
	}
	defer Resp.Body.Close()
	Result := &FetchResult{
		Success:      false,
		Robots:       nil,
		Error:        nil,
		ResponseCode: Resp.StatusCode,
		FetchTime:    time.Since(Start),
	}

	switch Resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusForbidden, http.StatusUnauthorized:
		PermissiveRobots, _ := robotstxt.FromString("")
		Result.Success = true
		Result.Robots = PermissiveRobots
		return Result, nil
	default:
		Result.Error = fmt.Errorf("unexpected status code: %d", Resp.StatusCode)
		return Result, nil
	}
	LimitedReader := io.LimitReader(Resp.Body, 500*1024) // 500KB limit
	Content, Err := io.ReadAll(LimitedReader)
	if Err != nil {
		Result.Error = fmt.Errorf("failed to read response body: %w", Err)
		return Result, nil
	}
	if len(Content) >= 500*1024 {
		Result.Error = fmt.Errorf("robots.txt too large (>500KB)")
		return Result, nil
	}
	Robots, Err := ParseRobotsContent(Content)
	if Err != nil {
		Result.Error = fmt.Errorf("failed to parse robots.txt: %w", Err)
		return Result, nil
	}
	Result.Success = true
	Result.Robots = Robots
	return Result, nil
}

// GetPerHostMutex returns a mutex for the given host to prevent concurrent fetching
func (P *Parser) GetPerHostMutex(Host string) *sync.Mutex {
	P.MutexLock.Lock()
	defer P.MutexLock.Unlock()
	if Mutex, Exists := P.FetchMutex[Host]; Exists {
		return Mutex
	}
	Mutex := &sync.Mutex{}
	P.FetchMutex[Host] = Mutex
	return Mutex
}

// IsExpired checks if a cache entry has expired
func (P *Parser) IsExpired(Entry *CacheEntry) bool {
	if Entry == nil {
		return true
	}
	return time.Now().After(Entry.ExpiresAt)
}

// GetCachedRobots returns cached robots.txt if it exists and is not expired
func (P *Parser) GetCachedRobots(Host string) (*robotstxt.RobotsData, bool) {
	P.CacheMutex.RLock()
	defer P.CacheMutex.RUnlock()
	Entry, Exists := P.Cache[Host]
	if !Exists {
		return nil, false
	}
	if P.IsExpired(Entry) {
		return nil, false
	}
	return Entry.Robots, true
}

// SetCachedRobots stores robots.txt in the cache with expiration
func (P *Parser) SetCachedRobots(Host string, Robots *robotstxt.RobotsData) {
	P.CacheMutex.Lock()
	defer P.CacheMutex.Unlock()
	Now := time.Now()
	Entry := &CacheEntry{
		Robots:    Robots,
		FetchedAt: Now,
		ExpiresAt: Now.Add(P.CacheTTL),
	}
	P.Cache[Host] = Entry
}

// ClearCache removes all entries from the robots.txt cache
func (P *Parser) ClearCache() {
	P.CacheMutex.Lock()
	defer P.CacheMutex.Unlock()
	P.Cache = make(map[string]*CacheEntry)
}

// ClearExpired removes expired entries from the cache
func (P *Parser) ClearExpired() int {
	P.CacheMutex.Lock()
	defer P.CacheMutex.Unlock()
	RemovedCount := 0
	for Host, Entry := range P.Cache {
		if P.IsExpired(Entry) {
			delete(P.Cache, Host)
			RemovedCount++
		}
	}
	return RemovedCount
}

// GetCacheStats returns statistics about the robots.txt cache
func (P *Parser) GetCacheStats() CacheStats {
	P.CacheMutex.RLock()
	defer P.CacheMutex.RUnlock()
	TotalEntries := len(P.Cache)
	ExpiredEntries := 0
	var OldestEntry, NewestEntry time.Time
	var TotalAge time.Duration

	if TotalEntries == 0 {
		return CacheStats{
			TotalEntries:   0,
			ExpiredEntries: 0,
			OldestEntry:    time.Time{},
			NewestEntry:    time.Time{},
			CacheHitRate:   0.0,
			AverageAge:     0,
		}
	}
	First := true
	for _, Entry := range P.Cache {
		if P.IsExpired(Entry) {
			ExpiredEntries++
		}

		FetchTime := Entry.FetchedAt
		if First {
			OldestEntry = FetchTime
			NewestEntry = FetchTime
			First = false
		} else {
			if FetchTime.Before(OldestEntry) {
				OldestEntry = FetchTime
			}
			if FetchTime.After(NewestEntry) {
				NewestEntry = FetchTime
			}
		}

		TotalAge += time.Since(FetchTime)
	}

	AverageAge := time.Duration(0)
	if TotalEntries > 0 {
		AverageAge = TotalAge / time.Duration(TotalEntries)
	}
	return CacheStats{
		TotalEntries:   TotalEntries,
		ExpiredEntries: ExpiredEntries,
		OldestEntry:    OldestEntry,
		NewestEntry:    NewestEntry,
		CacheHitRate:   0.0, // Not tracking hits/misses yet
		AverageAge:     AverageAge,
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

// BuildRobotsURL constructs the robots.txt URL for a given host
func BuildRobotsURL(Host string) string {
	Host = strings.TrimSpace(Host)

	if Host == "" {
		return ""
	}

	// Ensure host has a scheme
	if !strings.HasPrefix(Host, "http://") && !strings.HasPrefix(Host, "https://") {
		Host = "http://" + Host
	}

	// Use frontier URL parsing utilities
	U, Err := frontier.ParseURL(Host)
	if Err != nil {
		// Fallback for invalid URLs
		if !strings.HasSuffix(Host, "/") {
			Host += "/"
		}
		return Host + "robots.txt"
	}

	U.Path = "/robots.txt"
	U.RawQuery = ""
	U.Fragment = ""

	return U.String()
}

// ParseRobotsContent parses robots.txt content and returns RobotsData
func ParseRobotsContent(Content []byte) (*robotstxt.RobotsData, error) {
	if len(Content) == 0 {
		R, _ := robotstxt.FromString("") // ignore error, empty string is valid
		return R, nil
	}
	R, Err := robotstxt.FromBytes(Content)
	if Err != nil {
		R, _ = robotstxt.FromString("")
		return R, fmt.Errorf("failed to parse robots.txt, returning permissive: %w", Err)
	}

	if R == nil {
		R, _ = robotstxt.FromString("")
		return R, fmt.Errorf("robots.txt parsing returned nil data, returning permissive")
	}

	return R, nil
}

// Close gracefully shuts down the parser and cleans up resources
func (P *Parser) Close() error {
	P.ClearCache()
	if P.HTTPClient != nil {
		P.HTTPClient.Close()
	}
	P.MutexLock.Lock()
	P.FetchMutex = make(map[string]*sync.Mutex)
	P.MutexLock.Unlock()

	return nil
}
