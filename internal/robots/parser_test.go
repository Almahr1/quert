package robots

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mock robots.txt content for testing
const (
	sampleRobotsTxt = `User-agent: *
Disallow: /private/
Disallow: /temp/
Allow: /public/

User-agent: TestCrawler
Disallow: /admin/
Crawl-delay: 1

Sitemap: https://example.com/sitemap.xml
`

	strictRobotsTxt = `User-agent: *
Disallow: /
`

	emptyRobotsTxt = ``

	permissiveRobotsTxt = `User-agent: *
Allow: /
`
)

func createTestParser() *Parser {
	config := Config{
		UserAgent:   "TestCrawler/1.0",
		CacheTTL:    24 * time.Hour,
		HTTPTimeout: 5 * time.Second,
		MaxSize:     500 * 1024, // 500KB
	}
	return NewParser(config)
}

func TestNewParser(t *testing.T) {
	tests := []struct {
		name            string
		config          Config
		expectedUA      string
		expectedTTL     time.Duration
		expectedTimeout time.Duration
		expectedMaxSize int64
	}{
		{
			name:            "default values",
			config:          Config{},
			expectedUA:      "*",
			expectedTTL:     24 * time.Hour,
			expectedTimeout: 30 * time.Second,
			expectedMaxSize: 500 * 1024,
		},
		{
			name: "custom values",
			config: Config{
				UserAgent:   "CustomBot/2.0",
				CacheTTL:    1 * time.Hour,
				HTTPTimeout: 10 * time.Second,
				MaxSize:     1024 * 1024,
			},
			expectedUA:      "CustomBot/2.0",
			expectedTTL:     1 * time.Hour,
			expectedTimeout: 10 * time.Second,
			expectedMaxSize: 1024 * 1024,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := NewParser(tt.config)

			assert.NotNil(t, parser)
			assert.Equal(t, tt.expectedUA, parser.userAgent)
			assert.Equal(t, tt.expectedTTL, parser.cacheTTL)
			assert.Equal(t, tt.expectedTimeout, parser.client.Timeout)
			assert.NotNil(t, parser.cache)
			assert.NotNil(t, parser.fetchMutex)
		})
	}
}

func TestIsAllowed(t *testing.T) {
	tests := []struct {
		name               string
		robotsTxt          string
		statusCode         int
		url                string
		userAgent          string
		expectedAllowed    bool
		expectedCrawlDelay time.Duration
		wantErr            bool
	}{
		{
			name:               "allowed URL with permissive robots.txt",
			robotsTxt:          permissiveRobotsTxt,
			statusCode:         200,
			url:                "https://example.com/public/page.html",
			userAgent:          "TestBot/1.0",
			expectedAllowed:    true,
			expectedCrawlDelay: 0,
			wantErr:            false,
		},
		{
			name:               "disallowed URL with strict robots.txt",
			robotsTxt:          strictRobotsTxt,
			statusCode:         200,
			url:                "https://example.com/any-page.html",
			userAgent:          "TestBot/1.0",
			expectedAllowed:    false,
			expectedCrawlDelay: 0,
			wantErr:            false,
		},
		{
			name:               "404 robots.txt - should be permissive",
			robotsTxt:          "",
			statusCode:         404,
			url:                "https://example.com/any-page.html",
			userAgent:          "TestBot/1.0",
			expectedAllowed:    true,
			expectedCrawlDelay: 0,
			wantErr:            false,
		},
		{
			name:               "empty URL",
			robotsTxt:          sampleRobotsTxt,
			statusCode:         200,
			url:                "",
			userAgent:          "TestBot/1.0",
			expectedAllowed:    false,
			expectedCrawlDelay: 0,
			wantErr:            true,
		},
		{
			name:               "specific user agent with crawl delay",
			robotsTxt:          sampleRobotsTxt,
			statusCode:         200,
			url:                "https://example.com/public/page.html",
			userAgent:          "TestCrawler",
			expectedAllowed:    true,
			expectedCrawlDelay: 1 * time.Second,
			wantErr:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/robots.txt" {
					w.WriteHeader(tt.statusCode)
					if tt.statusCode == 200 {
						w.Write([]byte(tt.robotsTxt))
					}
				} else {
					w.WriteHeader(404)
				}
			}))
			defer server.Close()

			config := Config{
				UserAgent:   tt.userAgent,
				CacheTTL:    1 * time.Minute, // Short TTL for testing
				HTTPTimeout: 5 * time.Second,
			}
			parser := NewParser(config)

			testURL := tt.url
			if testURL != "" {
				testURL = strings.Replace(testURL, "https://example.com", server.URL, 1)
			}

			ctx := context.Background()
			result, err := parser.IsAllowed(ctx, testURL)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.expectedAllowed, result.Allowed)
			assert.Equal(t, tt.expectedCrawlDelay, result.CrawlDelay)
			assert.NotNil(t, result.Sitemaps)

			if !result.Allowed {
				assert.NotEmpty(t, result.DisallowedBy)
			} else {
				assert.Empty(t, result.DisallowedBy)
			}
		})
	}
}

func TestGetRobots(t *testing.T) {
	tests := []struct {
		name       string
		robotsTxt  string
		statusCode int
		host       string
		wantErr    bool
	}{
		{
			name:       "successful fetch",
			robotsTxt:  sampleRobotsTxt,
			statusCode: 200,
			host:       "example.com",
			wantErr:    false,
		},
		{
			name:       "404 not found",
			robotsTxt:  "",
			statusCode: 404,
			host:       "example.com",
			wantErr:    false, // 404 should not be an error - returns permissive robots
		},
		{
			name:       "empty host",
			robotsTxt:  sampleRobotsTxt,
			statusCode: 200,
			host:       "",
			wantErr:    true,
		},
		{
			name:       "host with port",
			robotsTxt:  sampleRobotsTxt,
			statusCode: 200,
			host:       "example.com:8080",
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/robots.txt" {
					w.WriteHeader(tt.statusCode)
					if tt.statusCode == 200 {
						w.Write([]byte(tt.robotsTxt))
					}
				}
			}))
			defer server.Close()

			parser := createTestParser()
			ctx := context.Background()

			testHost := tt.host
			if testHost != "" && testHost != "example.com" && testHost != "example.com:8080" {
				testHost = tt.host
			} else if testHost != "" {
				serverURL := strings.TrimPrefix(server.URL, "http://")
				if strings.Contains(testHost, ":") {
					testHost = serverURL
				} else {
					testHost = strings.Split(serverURL, ":")[0]
				}
			}

			robots, err := parser.GetRobots(ctx, testHost)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.NotNil(t, robots)
		})
	}
}

func TestCaching(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			requestCount++
			w.WriteHeader(200)
			w.Write([]byte(sampleRobotsTxt))
		}
	}))
	defer server.Close()

	// Create parser with short TTL for testing
	config := Config{
		UserAgent:   "TestBot/1.0",
		CacheTTL:    100 * time.Millisecond,
		HTTPTimeout: 5 * time.Second,
	}
	parser := NewParser(config)

	ctx := context.Background()
	host := strings.TrimPrefix(server.URL, "http://")

	robots1, err := parser.GetRobots(ctx, host)
	require.NoError(t, err)
	assert.NotNil(t, robots1)
	assert.Equal(t, 1, requestCount, "First request should fetch from server")

	robots2, err := parser.GetRobots(ctx, host)
	require.NoError(t, err)
	assert.NotNil(t, robots2)
	assert.Equal(t, 1, requestCount, "Second request should use cache")

	time.Sleep(150 * time.Millisecond)

	robots3, err := parser.GetRobots(ctx, host)
	require.NoError(t, err)
	assert.NotNil(t, robots3)
	assert.Equal(t, 2, requestCount, "Third request after expiry should fetch from server")
}

func TestCacheOperations(t *testing.T) {
	parser := createTestParser()

	stats := parser.GetCacheStats()
	assert.Equal(t, 0, stats.TotalEntries)
	assert.Equal(t, 0, stats.ExpiredEntries)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(sampleRobotsTxt))
	}))
	defer server.Close()

	ctx := context.Background()
	host := strings.TrimPrefix(server.URL, "http://")

	_, err := parser.GetRobots(ctx, host)
	require.NoError(t, err)

	stats = parser.GetCacheStats()
	assert.Equal(t, 1, stats.TotalEntries)
	assert.Equal(t, 0, stats.ExpiredEntries)
	assert.False(t, stats.OldestEntry.IsZero())
	assert.False(t, stats.NewestEntry.IsZero())

	parser.ClearCache()
	stats = parser.GetCacheStats()
	assert.Equal(t, 0, stats.TotalEntries)
}

func TestClearExpired(t *testing.T) {
	// Create parser with very short TTL
	config := Config{
		UserAgent:   "TestBot/1.0",
		CacheTTL:    1 * time.Millisecond,
		HTTPTimeout: 5 * time.Second,
	}
	parser := NewParser(config)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(sampleRobotsTxt))
	}))
	defer server.Close()

	ctx := context.Background()
	host := strings.TrimPrefix(server.URL, "http://")

	_, err := parser.GetRobots(ctx, host)
	require.NoError(t, err)

	time.Sleep(5 * time.Millisecond)

	removedCount := parser.ClearExpired()
	assert.Equal(t, 1, removedCount)

	stats := parser.GetCacheStats()
	assert.Equal(t, 0, stats.TotalEntries)
}

func TestExtractHostFromURL(t *testing.T) {
	tests := []struct {
		name         string
		url          string
		expectedHost string
		wantErr      bool
	}{
		{
			name:         "valid HTTP URL",
			url:          "http://example.com/path",
			expectedHost: "example.com",
			wantErr:      false,
		},
		{
			name:         "valid HTTPS URL",
			url:          "https://subdomain.example.com:8080/path",
			expectedHost: "subdomain.example.com:8080",
			wantErr:      false,
		},
		{
			name:         "URL without scheme",
			url:          "example.com/path",
			expectedHost: "example.com",
			wantErr:      false,
		},
		{
			name:         "empty URL",
			url:          "",
			expectedHost: "",
			wantErr:      true,
		},
		{
			name:         "invalid URL",
			url:          "://invalid",
			expectedHost: "",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, err := extractHostFromURL(tt.url)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expectedHost, host)
		})
	}
}

func TestBuildRobotsURL(t *testing.T) {
	tests := []struct {
		name        string
		host        string
		expectedURL string
	}{
		{
			name:        "simple host",
			host:        "example.com",
			expectedURL: "http://example.com/robots.txt",
		},
		{
			name:        "host with HTTP scheme",
			host:        "http://example.com",
			expectedURL: "http://example.com/robots.txt",
		},
		{
			name:        "host with HTTPS scheme",
			host:        "https://example.com",
			expectedURL: "https://example.com/robots.txt",
		},
		{
			name:        "host with port",
			host:        "example.com:8080",
			expectedURL: "http://example.com:8080/robots.txt",
		},
		{
			name:        "empty host",
			host:        "",
			expectedURL: "",
		},
		{
			name:        "host with path (should be normalized)",
			host:        "http://example.com/some/path",
			expectedURL: "http://example.com/robots.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := buildRobotsURL(tt.host)
			assert.Equal(t, tt.expectedURL, url)
		})
	}
}

func TestParseRobotsContent(t *testing.T) {
	tests := []struct {
		name      string
		content   []byte
		wantErr   bool
		expectNil bool
	}{
		{
			name:      "valid robots.txt",
			content:   []byte(sampleRobotsTxt),
			wantErr:   false,
			expectNil: false,
		},
		{
			name:      "empty content",
			content:   []byte{},
			wantErr:   false,
			expectNil: false,
		},
		{
			name:      "invalid content (should fallback to permissive)",
			content:   []byte("invalid robots content\x00\x01"),
			wantErr:   false, // Should not error, returns permissive robots
			expectNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			robots, err := parseRobotsContent(tt.content)

			if tt.expectNil {
				assert.Nil(t, robots)
			} else {
				assert.NotNil(t, robots)
			}

			if tt.wantErr {
				assert.Error(t, err)
			}
		})
	}
}

func TestClose(t *testing.T) {
	parser := createTestParser()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(sampleRobotsTxt))
	}))
	defer server.Close()

	ctx := context.Background()
	host := strings.TrimPrefix(server.URL, "http://")

	_, err := parser.GetRobots(ctx, host)
	require.NoError(t, err)

	stats := parser.GetCacheStats()
	assert.Equal(t, 1, stats.TotalEntries)

	err = parser.Close()
	assert.NoError(t, err)

	stats = parser.GetCacheStats()
	assert.Equal(t, 0, stats.TotalEntries)
}

func TestConcurrentAccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(200)
		w.Write([]byte(sampleRobotsTxt))
	}))
	defer server.Close()

	parser := createTestParser()
	ctx := context.Background()
	host := strings.TrimPrefix(server.URL, "http://")

	const numGoroutines = 10
	results := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			_, err := parser.GetRobots(ctx, host)
			results <- err
		}()
	}

	for i := 0; i < numGoroutines; i++ {
		err := <-results
		assert.NoError(t, err)
	}

	stats := parser.GetCacheStats()
	assert.Equal(t, 1, stats.TotalEntries)
}
