package robots

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Almahr1/quert/internal/client"
	"github.com/Almahr1/quert/internal/config"
	"github.com/Almahr1/quert/internal/frontier"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Mock robots.txt content for testing
const (
	SampleRobotsTxt = `User-agent: *
Disallow: /private/
Disallow: /temp/
Allow: /public/

User-agent: TestCrawler
Disallow: /admin/
Crawl-delay: 1

Sitemap: https://example.com/sitemap.xml
`

	StrictRobotsTxt = `User-agent: *
Disallow: /
`

	EmptyRobotsTxt = ``

	PermissiveRobotsTxt = `User-agent: *
Allow: /
`
)

func CreateTestParser() *Parser {
	RobotsConfig := Config{
		UserAgent:   "TestCrawler/1.0",
		CacheTTL:    24 * time.Hour,
		HTTPTimeout: 5 * time.Second,
		MaxSize:     500 * 1024, // 500KB
	}

	// Create HTTP client for testing
	HTTPConfig := &config.HTTPConfig{
		MaxIdleConnections:        100,
		MaxIdleConnectionsPerHost: 10,
		IdleConnectionTimeout:     30 * time.Second,
		DisableKeepAlives:         false,
		Timeout:                   5 * time.Second,
		DialTimeout:               3 * time.Second,
		TlsHandshakeTimeout:       5 * time.Second,
		ResponseHeaderTimeout:     5 * time.Second,
		DisableCompression:        false,
		AcceptEncoding:            "gzip, deflate",
	}

	Logger, _ := zap.NewDevelopment()
	HTTPClient := client.NewHTTPClient(HTTPConfig, Logger)

	return NewParser(RobotsConfig, HTTPClient)
}

func TestNewParser(t *testing.T) {
	Tests := []struct {
		Name            string
		Config          Config
		ExpectedUA      string
		ExpectedTTL     time.Duration
		ExpectedTimeout time.Duration
		ExpectedMaxSize int64
	}{
		{
			Name:            "default values",
			Config:          Config{},
			ExpectedUA:      "*",
			ExpectedTTL:     24 * time.Hour,
			ExpectedTimeout: 30 * time.Second,
			ExpectedMaxSize: 500 * 1024,
		},
		{
			Name: "custom values",
			Config: Config{
				UserAgent:   "CustomBot/2.0",
				CacheTTL:    1 * time.Hour,
				HTTPTimeout: 10 * time.Second,
				MaxSize:     1024 * 1024,
			},
			ExpectedUA:      "CustomBot/2.0",
			ExpectedTTL:     1 * time.Hour,
			ExpectedTimeout: 10 * time.Second,
			ExpectedMaxSize: 1024 * 1024,
		},
	}

	for _, TT := range Tests {
		t.Run(TT.Name, func(t *testing.T) {
			// Create HTTP client for test
			HTTPConfig := &config.HTTPConfig{
				Timeout: TT.ExpectedTimeout,
			}
			Logger, _ := zap.NewDevelopment()
			HTTPClient := client.NewHTTPClient(HTTPConfig, Logger)

			Parser := NewParser(TT.Config, HTTPClient)

			assert.NotNil(t, Parser)
			assert.Equal(t, TT.ExpectedUA, Parser.UserAgent)
			assert.Equal(t, TT.ExpectedTTL, Parser.CacheTTL)
			assert.NotNil(t, Parser.Cache)
			assert.NotNil(t, Parser.FetchMutex)
		})
	}
}

func TestIsAllowed(t *testing.T) {
	Tests := []struct {
		Name               string
		RobotsTxt          string
		StatusCode         int
		URL                string
		UserAgent          string
		ExpectedAllowed    bool
		ExpectedCrawlDelay time.Duration
		WantErr            bool
	}{
		{
			Name:               "allowed URL with permissive robots.txt",
			RobotsTxt:          PermissiveRobotsTxt,
			StatusCode:         200,
			URL:                "https://example.com/public/page.html",
			UserAgent:          "TestBot/1.0",
			ExpectedAllowed:    true,
			ExpectedCrawlDelay: 0,
			WantErr:            false,
		},
		{
			Name:               "disallowed URL with strict robots.txt",
			RobotsTxt:          StrictRobotsTxt,
			StatusCode:         200,
			URL:                "https://example.com/any-page.html",
			UserAgent:          "TestBot/1.0",
			ExpectedAllowed:    false,
			ExpectedCrawlDelay: 0,
			WantErr:            false,
		},
		{
			Name:               "404 robots.txt - should be permissive",
			RobotsTxt:          "",
			StatusCode:         404,
			URL:                "https://example.com/any-page.html",
			UserAgent:          "TestBot/1.0",
			ExpectedAllowed:    true,
			ExpectedCrawlDelay: 0,
			WantErr:            false,
		},
		{
			Name:               "empty URL",
			RobotsTxt:          SampleRobotsTxt,
			StatusCode:         200,
			URL:                "",
			UserAgent:          "TestBot/1.0",
			ExpectedAllowed:    false,
			ExpectedCrawlDelay: 0,
			WantErr:            true,
		},
		{
			Name:               "specific user agent with crawl delay",
			RobotsTxt:          SampleRobotsTxt,
			StatusCode:         200,
			URL:                "https://example.com/public/page.html",
			UserAgent:          "TestCrawler",
			ExpectedAllowed:    true,
			ExpectedCrawlDelay: 1 * time.Second,
			WantErr:            false,
		},
	}

	for _, TT := range Tests {
		t.Run(TT.Name, func(t *testing.T) {
			Server := httptest.NewServer(http.HandlerFunc(func(W http.ResponseWriter, R *http.Request) {
				if R.URL.Path == "/robots.txt" {
					W.WriteHeader(TT.StatusCode)
					if TT.StatusCode == 200 {
						W.Write([]byte(TT.RobotsTxt))
					}
				} else {
					W.WriteHeader(404)
				}
			}))
			defer Server.Close()

			RobotsConfig := Config{
				UserAgent:   TT.UserAgent,
				CacheTTL:    1 * time.Minute, // Short TTL for testing
				HTTPTimeout: 5 * time.Second,
			}
			HTTPConfig := &config.HTTPConfig{Timeout: 5 * time.Second}
			Logger, _ := zap.NewDevelopment()
			HTTPClient := client.NewHTTPClient(HTTPConfig, Logger)
			Parser := NewParser(RobotsConfig, HTTPClient)

			TestURL := TT.URL
			if TestURL != "" {
				TestURL = strings.Replace(TestURL, "https://example.com", Server.URL, 1)
			}

			Ctx := context.Background()
			Result, Err := Parser.IsAllowed(Ctx, TestURL)

			if TT.WantErr {
				assert.Error(t, Err)
				return
			}

			require.NoError(t, Err)
			require.NotNil(t, Result)
			assert.Equal(t, TT.ExpectedAllowed, Result.Allowed)
			assert.Equal(t, TT.ExpectedCrawlDelay, Result.CrawlDelay)
			assert.NotNil(t, Result.Sitemaps)

			if !Result.Allowed {
				assert.NotEmpty(t, Result.DisallowedBy)
			} else {
				assert.Empty(t, Result.DisallowedBy)
			}
		})
	}
}

func TestGetRobots(t *testing.T) {
	Tests := []struct {
		Name       string
		RobotsTxt  string
		StatusCode int
		Host       string
		WantErr    bool
	}{
		{
			Name:       "successful fetch",
			RobotsTxt:  SampleRobotsTxt,
			StatusCode: 200,
			Host:       "example.com",
			WantErr:    false,
		},
		{
			Name:       "404 not found",
			RobotsTxt:  "",
			StatusCode: 404,
			Host:       "example.com",
			WantErr:    false, // 404 should not be an error - returns permissive robots
		},
		{
			Name:       "empty host",
			RobotsTxt:  SampleRobotsTxt,
			StatusCode: 200,
			Host:       "",
			WantErr:    true,
		},
		{
			Name:       "host with port",
			RobotsTxt:  SampleRobotsTxt,
			StatusCode: 200,
			Host:       "example.com:8080",
			WantErr:    false,
		},
	}

	for _, TT := range Tests {
		t.Run(TT.Name, func(t *testing.T) {
			Server := httptest.NewServer(http.HandlerFunc(func(W http.ResponseWriter, R *http.Request) {
				if R.URL.Path == "/robots.txt" {
					W.WriteHeader(TT.StatusCode)
					if TT.StatusCode == 200 {
						W.Write([]byte(TT.RobotsTxt))
					}
				}
			}))
			defer Server.Close()

			Parser := CreateTestParser()
			Ctx := context.Background()

			TestHost := TT.Host
			if TestHost != "" && TestHost != "example.com" && TestHost != "example.com:8080" {
				TestHost = TT.Host
			} else if TestHost != "" {
				ServerURL := strings.TrimPrefix(Server.URL, "http://")
				if strings.Contains(TestHost, ":") {
					TestHost = ServerURL
				} else {
					TestHost = strings.Split(ServerURL, ":")[0]
				}
			}

			Robots, Err := Parser.GetRobots(Ctx, TestHost)

			if TT.WantErr {
				assert.Error(t, Err)
				return
			}

			require.NoError(t, Err)
			assert.NotNil(t, Robots)
		})
	}
}

func TestCaching(t *testing.T) {
	RequestCount := 0
	Server := httptest.NewServer(http.HandlerFunc(func(W http.ResponseWriter, R *http.Request) {
		if R.URL.Path == "/robots.txt" {
			RequestCount++
			W.WriteHeader(200)
			W.Write([]byte(SampleRobotsTxt))
		}
	}))
	defer Server.Close()

	// Create parser with short TTL for testing
	RobotsConfig := Config{
		UserAgent:   "TestBot/1.0",
		CacheTTL:    100 * time.Millisecond,
		HTTPTimeout: 5 * time.Second,
	}
	HTTPConfig := &config.HTTPConfig{Timeout: 5 * time.Second}
	Logger, _ := zap.NewDevelopment()
	HTTPClient := client.NewHTTPClient(HTTPConfig, Logger)
	Parser := NewParser(RobotsConfig, HTTPClient)

	Ctx := context.Background()
	Host := strings.TrimPrefix(Server.URL, "http://")

	Robots1, Err := Parser.GetRobots(Ctx, Host)
	require.NoError(t, Err)
	assert.NotNil(t, Robots1)
	assert.Equal(t, 1, RequestCount, "First request should fetch from server")

	Robots2, Err := Parser.GetRobots(Ctx, Host)
	require.NoError(t, Err)
	assert.NotNil(t, Robots2)
	assert.Equal(t, 1, RequestCount, "Second request should use cache")

	time.Sleep(150 * time.Millisecond)

	Robots3, Err := Parser.GetRobots(Ctx, Host)
	require.NoError(t, Err)
	assert.NotNil(t, Robots3)
	assert.Equal(t, 2, RequestCount, "Third request after expiry should fetch from server")
}

func TestCacheOperations(t *testing.T) {
	Parser := CreateTestParser()

	Stats := Parser.GetCacheStats()
	assert.Equal(t, 0, Stats.TotalEntries)
	assert.Equal(t, 0, Stats.ExpiredEntries)

	Server := httptest.NewServer(http.HandlerFunc(func(W http.ResponseWriter, R *http.Request) {
		W.WriteHeader(200)
		W.Write([]byte(SampleRobotsTxt))
	}))
	defer Server.Close()

	Ctx := context.Background()
	Host := strings.TrimPrefix(Server.URL, "http://")

	_, Err := Parser.GetRobots(Ctx, Host)
	require.NoError(t, Err)

	Stats = Parser.GetCacheStats()
	assert.Equal(t, 1, Stats.TotalEntries)
	assert.Equal(t, 0, Stats.ExpiredEntries)
	assert.False(t, Stats.OldestEntry.IsZero())
	assert.False(t, Stats.NewestEntry.IsZero())

	Parser.ClearCache()
	Stats = Parser.GetCacheStats()
	assert.Equal(t, 0, Stats.TotalEntries)
}

func TestClearExpired(t *testing.T) {
	// Create parser with very short TTL
	RobotsConfig := Config{
		UserAgent:   "TestBot/1.0",
		CacheTTL:    1 * time.Millisecond,
		HTTPTimeout: 5 * time.Second,
	}
	HTTPConfig := &config.HTTPConfig{Timeout: 5 * time.Second}
	Logger, _ := zap.NewDevelopment()
	HTTPClient := client.NewHTTPClient(HTTPConfig, Logger)
	Parser := NewParser(RobotsConfig, HTTPClient)

	Server := httptest.NewServer(http.HandlerFunc(func(W http.ResponseWriter, R *http.Request) {
		W.WriteHeader(200)
		W.Write([]byte(SampleRobotsTxt))
	}))
	defer Server.Close()

	Ctx := context.Background()
	Host := strings.TrimPrefix(Server.URL, "http://")

	_, Err := Parser.GetRobots(Ctx, Host)
	require.NoError(t, Err)

	time.Sleep(5 * time.Millisecond)

	RemovedCount := Parser.ClearExpired()
	assert.Equal(t, 1, RemovedCount)

	Stats := Parser.GetCacheStats()
	assert.Equal(t, 0, Stats.TotalEntries)
}

func TestExtractHostFromURL(t *testing.T) {
	Tests := []struct {
		Name         string
		URL          string
		ExpectedHost string
		WantErr      bool
	}{
		{
			Name:         "valid HTTP URL",
			URL:          "http://example.com/path",
			ExpectedHost: "example.com",
			WantErr:      false,
		},
		{
			Name:         "valid HTTPS URL",
			URL:          "https://subdomain.example.com:8080/path",
			ExpectedHost: "subdomain.example.com:8080",
			WantErr:      false,
		},
		{
			Name:         "URL without scheme",
			URL:          "example.com/path",
			ExpectedHost: "example.com",
			WantErr:      false,
		},
		{
			Name:         "empty URL",
			URL:          "",
			ExpectedHost: "",
			WantErr:      true,
		},
		{
			Name:         "invalid URL",
			URL:          "://invalid",
			ExpectedHost: "",
			WantErr:      true,
		},
	}

	for _, TT := range Tests {
		t.Run(TT.Name, func(t *testing.T) {
			Host, Err := frontier.ExtractHostFromURL(TT.URL)

			if TT.WantErr {
				assert.Error(t, Err)
				return
			}

			require.NoError(t, Err)
			assert.Equal(t, TT.ExpectedHost, Host)
		})
	}
}

func TestBuildRobotsURL(t *testing.T) {
	Tests := []struct {
		Name        string
		Host        string
		ExpectedURL string
	}{
		{
			Name:        "simple host",
			Host:        "example.com",
			ExpectedURL: "http://example.com/robots.txt",
		},
		{
			Name:        "host with HTTP scheme",
			Host:        "http://example.com",
			ExpectedURL: "http://example.com/robots.txt",
		},
		{
			Name:        "host with HTTPS scheme",
			Host:        "https://example.com",
			ExpectedURL: "https://example.com/robots.txt",
		},
		{
			Name:        "host with port",
			Host:        "example.com:8080",
			ExpectedURL: "http://example.com:8080/robots.txt",
		},
		{
			Name:        "empty host",
			Host:        "",
			ExpectedURL: "",
		},
		{
			Name:        "host with path (should be normalized)",
			Host:        "http://example.com/some/path",
			ExpectedURL: "http://example.com/robots.txt",
		},
	}

	for _, TT := range Tests {
		t.Run(TT.Name, func(t *testing.T) {
			URL := BuildRobotsURL(TT.Host)
			assert.Equal(t, TT.ExpectedURL, URL)
		})
	}
}

func TestParseRobotsContent(t *testing.T) {
	Tests := []struct {
		Name      string
		Content   []byte
		WantErr   bool
		ExpectNil bool
	}{
		{
			Name:      "valid robots.txt",
			Content:   []byte(SampleRobotsTxt),
			WantErr:   false,
			ExpectNil: false,
		},
		{
			Name:      "empty content",
			Content:   []byte{},
			WantErr:   false,
			ExpectNil: false,
		},
		{
			Name:      "invalid content (should fallback to permissive)",
			Content:   []byte("invalid robots content\x00\x01"),
			WantErr:   false, // Should not error, returns permissive robots
			ExpectNil: false,
		},
	}

	for _, TT := range Tests {
		t.Run(TT.Name, func(t *testing.T) {
			Robots, Err := ParseRobotsContent(TT.Content)

			if TT.ExpectNil {
				assert.Nil(t, Robots)
			} else {
				assert.NotNil(t, Robots)
			}

			if TT.WantErr {
				assert.Error(t, Err)
			}
		})
	}
}

func TestClose(t *testing.T) {
	Parser := CreateTestParser()

	Server := httptest.NewServer(http.HandlerFunc(func(W http.ResponseWriter, R *http.Request) {
		W.WriteHeader(200)
		W.Write([]byte(SampleRobotsTxt))
	}))
	defer Server.Close()

	Ctx := context.Background()
	Host := strings.TrimPrefix(Server.URL, "http://")

	_, Err := Parser.GetRobots(Ctx, Host)
	require.NoError(t, Err)

	Stats := Parser.GetCacheStats()
	assert.Equal(t, 1, Stats.TotalEntries)

	Err = Parser.Close()
	assert.NoError(t, Err)

	Stats = Parser.GetCacheStats()
	assert.Equal(t, 0, Stats.TotalEntries)
}

func TestConcurrentAccess(t *testing.T) {
	Server := httptest.NewServer(http.HandlerFunc(func(W http.ResponseWriter, R *http.Request) {
		time.Sleep(10 * time.Millisecond)
		W.WriteHeader(200)
		W.Write([]byte(SampleRobotsTxt))
	}))
	defer Server.Close()

	Parser := CreateTestParser()
	Ctx := context.Background()
	Host := strings.TrimPrefix(Server.URL, "http://")

	const NumGoroutines = 10
	Results := make(chan error, NumGoroutines)

	for I := 0; I < NumGoroutines; I++ {
		go func() {
			_, Err := Parser.GetRobots(Ctx, Host)
			Results <- Err
		}()
	}

	for I := 0; I < NumGoroutines; I++ {
		Err := <-Results
		assert.NoError(t, Err)
	}

	Stats := Parser.GetCacheStats()
	assert.Equal(t, 1, Stats.TotalEntries)
}
