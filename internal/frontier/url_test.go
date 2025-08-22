package frontier

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestURLNormalizer_Normalize(t *testing.T) {
	normalizer := NewURLNormalizer()

	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{
			name:     "basic URL normalization",
			input:    "HTTP://EXAMPLE.COM/Path/../Page?param=value#fragment",
			expected: "http://example.com/Page?param=value",
			wantErr:  false,
		},
		{
			name:     "remove default port",
			input:    "https://example.com:443/page",
			expected: "https://example.com/page",
			wantErr:  false,
		},
		{
			name:     "sort query parameters",
			input:    "https://example.com/?z=3&a=1&b=2",
			expected: "https://example.com/?a=1&b=2&z=3",
			wantErr:  false,
		},
		{
			name:     "remove blacklisted parameters",
			input:    "https://example.com/?utm_source=test&param=value&utm_medium=email",
			expected: "https://example.com/?param=value",
			wantErr:  false,
		},
		{
			name:     "invalid URL",
			input:    "://invalid",
			expected: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := normalizer.Normalize(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Normalize() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if result != tt.expected {
				t.Errorf("Normalize() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestURLNormalizer_NormalizeBatch(t *testing.T) {
	normalizer := NewURLNormalizer()

	urls := []string{
		"HTTP://EXAMPLE.COM/page1",
		"https://test.com:443/page2",
		"://invalid",
		"https://site.com/page3?utm_source=test&param=value",
	}

	results, errors := normalizer.NormalizeBatch(urls)

	if len(results) != len(urls) {
		t.Errorf("Expected %d results, got %d", len(urls), len(results))
	}

	// Check that valid URLs were normalized
	if !strings.Contains(results[0], "http://example.com") {
		t.Errorf("First URL not properly normalized: %s", results[0])
	}

	// Check that invalid URL produced an error
	if errors[2] == nil {
		t.Errorf("Expected error for invalid URL, got nil")
	}
}

func TestURLValidator_IsValid(t *testing.T) {
	validator := NewURLValidator()

	tests := []struct {
		name     string
		urlInfo  *URLInfo
		expected bool
	}{
		{
			name: "valid HTTP URL",
			urlInfo: &URLInfo{
				URL:         "http://example.com/page",
				ContentType: "text/html",
			},
			expected: true,
		},
		{
			name: "blocked domain",
			urlInfo: &URLInfo{
				URL: "http://localhost/page",
			},
			expected: false,
		},
		{
			name: "unsupported scheme",
			urlInfo: &URLInfo{
				URL: "ftp://example.com/file",
			},
			expected: false,
		},
		{
			name: "blocked content type",
			urlInfo: &URLInfo{
				URL:         "http://example.com/image.jpg",
				ContentType: "image/jpeg",
			},
			expected: false,
		},
		{
			name:     "nil URLInfo",
			urlInfo:  nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validator.IsValid(tt.urlInfo)
			if result != tt.expected {
				t.Errorf("IsValid() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestURLValidator_ValidateScheme(t *testing.T) {
	validator := NewURLValidator()

	tests := []struct {
		name     string
		scheme   string
		expected bool
	}{
		{"valid http", "http", true},
		{"valid https", "https", true},
		{"invalid ftp", "ftp", false},
		{"invalid empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validator.ValidateScheme(tt.scheme)
			if result != tt.expected {
				t.Errorf("ValidateScheme() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestURLValidator_ValidateDomain(t *testing.T) {
	validator := NewURLValidator()

	tests := []struct {
		name     string
		domain   string
		expected bool
	}{
		{"valid domain", "example.com", true},
		{"blocked localhost", "localhost", false},
		{"blocked with subdomain", "api.localhost", false},
		{"domain with port", "example.com:8080", true},
		{"empty domain", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validator.ValidateDomain(tt.domain)
			if result != tt.expected {
				t.Errorf("ValidateDomain() = %v, want %v for domain %s", result, tt.expected, tt.domain)
			}
		})
	}
}

func TestURLValidator_ValidateContentType(t *testing.T) {
	validator := NewURLValidator()

	tests := []struct {
		name        string
		contentType string
		expected    bool
	}{
		{"valid HTML", "text/html", true},
		{"valid HTML with charset", "text/html; charset=utf-8", true},
		{"blocked image", "image/jpeg", false},
		{"blocked PDF", "application/pdf", false},
		{"empty content type", "", true}, // No content type to validate
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validator.ValidateContentType(tt.contentType)
			if result != tt.expected {
				t.Errorf("ValidateContentType() = %v, want %v for %s", result, tt.expected, tt.contentType)
			}
		})
	}
}

func TestURLDeduplicator(t *testing.T) {
	dedup := NewURLDeduplicator()

	// Test URL deduplication
	url1 := "http://example.com/page1"
	url2 := "http://example.com/page2"

	// Initially, no URLs should be duplicates
	if dedup.IsDuplicate(url1) {
		t.Error("Expected URL1 to not be duplicate initially")
	}

	// Add URL1
	dedup.AddURL(url1)

	// Now URL1 should be a duplicate
	if !dedup.IsDuplicate(url1) {
		t.Error("Expected URL1 to be duplicate after adding")
	}

	// URL2 should still not be a duplicate
	if dedup.IsDuplicate(url2) {
		t.Error("Expected URL2 to not be duplicate")
	}

	// Test content hash deduplication
	hash1 := "abc123"
	hash2 := "def456"

	isDup, existingURL := dedup.IsContentDuplicate(hash1)
	if isDup || existingURL != "" {
		t.Error("Expected hash1 to not be duplicate initially")
	}

	dedup.AddContentHash(hash1, url1)

	isDup, existingURL = dedup.IsContentDuplicate(hash1)
	if !isDup || existingURL != url1 {
		t.Errorf("Expected hash1 to be duplicate with URL %s, got %s", url1, existingURL)
	}

	isDup, existingURL = dedup.IsContentDuplicate(hash2)
	if isDup || existingURL != "" {
		t.Error("Expected hash2 to not be duplicate")
	}
}

func TestExtractDomain(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected string
		wantErr  bool
	}{
		{"basic domain", "http://example.com/page", "example.com", false},
		{"domain with port", "https://example.com:8080/page", "example.com", false},
		{"subdomain", "http://api.example.com/data", "api.example.com", false},
		{"invalid URL", "not-a-url", "", true},
		{"URL without scheme", "//example.com", "example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ExtractDomain(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractDomain() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if result != tt.expected {
				t.Errorf("ExtractDomain() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestExtractSubdomain(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected string
		wantErr  bool
	}{
		{"no subdomain", "http://example.com/page", "", false},
		{"www subdomain", "http://www.example.com/page", "www", false},
		{"multiple subdomains", "http://api.v2.example.com/data", "api.v2", false},
		{"invalid URL", "not-a-url", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ExtractSubdomain(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractSubdomain() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if result != tt.expected {
				t.Errorf("ExtractSubdomain() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestExtractTLD(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected string
		wantErr  bool
	}{
		{"basic TLD", "http://example.com/page", "com", false},
		{"country TLD", "http://example.co.uk/page", "uk", false},
		{"subdomain", "http://www.example.org/page", "org", false},
		{"invalid URL", "not-a-url", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ExtractTLD(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractTLD() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if result != tt.expected {
				t.Errorf("ExtractTLD() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGetDomainInfo(t *testing.T) {
	url := "http://api.example.com/data"

	domainInfo, err := GetDomainInfo(url)
	if err != nil {
		t.Fatalf("GetDomainInfo() error = %v", err)
	}

	if domainInfo.Domain != "api.example.com" {
		t.Errorf("Expected domain api.example.com, got %s", domainInfo.Domain)
	}

	if domainInfo.Subdomain != "api" {
		t.Errorf("Expected subdomain api, got %s", domainInfo.Subdomain)
	}

	if domainInfo.TLD != "com" {
		t.Errorf("Expected TLD com, got %s", domainInfo.TLD)
	}

	if domainInfo.IsIP {
		t.Error("Expected IsIP to be false for domain")
	}
}

func TestIsIPAddress(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		expected bool
	}{
		{"IPv4 address", "192.168.1.1", true},
		{"IPv6 address", "2001:db8::1", true},
		{"domain name", "example.com", false},
		{"localhost", "localhost", false},
		{"empty string", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsIPAddress(tt.host)
			if result != tt.expected {
				t.Errorf("IsIPAddress() = %v, want %v for %s", result, tt.expected, tt.host)
			}
		})
	}
}

func TestIsValidDomain(t *testing.T) {
	tests := []struct {
		name     string
		domain   string
		expected bool
	}{
		{"valid domain", "example.com", true},
		{"valid subdomain", "api.example.com", true},
		{"invalid empty", "", false},
		{"valid with hyphens", "test-site.com", true},
		{"valid multiple levels", "api.v2.example.com", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsValidDomain(tt.domain)
			if result != tt.expected {
				t.Errorf("IsValidDomain() = %v, want %v for %s", result, tt.expected, tt.domain)
			}
		})
	}
}

func TestURLProcessor_Process(t *testing.T) {
	processor := NewURLProcessor()

	tests := []struct {
		name      string
		url       string
		wantErr   bool
		checkFunc func(*URLInfo) bool
	}{
		{
			name:    "valid URL processing",
			url:     "HTTP://EXAMPLE.COM/page?utm_source=test&param=value",
			wantErr: false,
			checkFunc: func(info *URLInfo) bool {
				return info != nil &&
					strings.Contains(info.NormalizedURL, "http://example.com") &&
					info.Domain == "example.com" &&
					!strings.Contains(info.NormalizedURL, "utm_source")
			},
		},
		{
			name:    "duplicate URL",
			url:     "HTTP://EXAMPLE.COM/page?utm_source=test&param=value",
			wantErr: true, // Should be duplicate on second process
		},
		{
			name:    "invalid URL",
			url:     "not-a-url",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := processor.Process(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("Process() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && tt.checkFunc != nil {
				if !tt.checkFunc(result) {
					t.Errorf("Process() result validation failed for %s", tt.url)
				}
			}
		})
	}
}

func TestURLProcessor_GetStatistics(t *testing.T) {
	processor := NewURLProcessor()

	// Process some URLs to generate statistics
	processor.Process("http://example.com/page1")
	processor.Process("http://example.com/page2")
	processor.Process("invalid-url")
	processor.Process("http://example.com/page1") // Duplicate

	stats := processor.GetStatistics()

	if stats["processed_count"] != 4 {
		t.Errorf("Expected processed_count 4, got %d", stats["processed_count"])
	}

	if stats["valid_count"] != 2 {
		t.Errorf("Expected valid_count 2, got %d", stats["valid_count"])
	}

	if stats["invalid_count"] != 1 {
		t.Errorf("Expected invalid_count 1, got %d", stats["invalid_count"])
	}

	if stats["duplicate_count"] != 1 {
		t.Errorf("Expected duplicate_count 1, got %d", stats["duplicate_count"])
	}
}

func TestCalculateContentHash(t *testing.T) {
	content1 := []byte("test content")
	content2 := []byte("different content")

	hash1 := CalculateContentHash(content1)
	hash2 := CalculateContentHash(content2)

	if hash1 == hash2 {
		t.Error("Expected different hashes for different content")
	}

	// Same content should produce same hash
	hash3 := CalculateContentHash(content1)
	if hash1 != hash3 {
		t.Error("Expected same hash for same content")
	}

	if len(hash1) != 64 { // SHA-256 hex string should be 64 characters
		t.Errorf("Expected hash length 64, got %d", len(hash1))
	}
}

func TestCalculateSimhash(t *testing.T) {
	tests := []struct {
		name     string
		content1 string
		content2 string
		similar  bool
	}{
		{
			name:     "identical content",
			content1: "This is a test document with some words",
			content2: "This is a test document with some words",
			similar:  true,
		},
		{
			name:     "similar content with minor changes",
			content1: "This is a test document with some words",
			content2: "This is a test document with some different words",
			similar:  true, // Should be similar due to shingling
		},
		{
			name:     "reordered words",
			content1: "apple banana cherry",
			content2: "cherry banana apple",
			similar:  false, // Character n-grams will be different
		},
		{
			name:     "completely different content",
			content1: "This is a test document with some words",
			content2: "Completely different content altogether here",
			similar:  false,
		},
		{
			name:     "empty content",
			content1: "",
			content2: "",
			similar:  true,
		},
		{
			name:     "case differences",
			content1: "Hello World",
			content2: "HELLO WORLD",
			similar:  true, // Should be identical after normalization
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash1 := CalculateSimhash(tt.content1)
			hash2 := CalculateSimhash(tt.content2)

			if tt.similar {
				if tt.content1 == tt.content2 {
					// Identical content should produce identical hashes
					assert.Equal(t, hash1, hash2, "Expected identical content to produce identical hashes")
				} else {
					// Similar content should have low Hamming distance
					distance := HammingDistance(hash1, hash2)
					assert.LessOrEqual(t, distance, 10, "Expected similar content to have low Hamming distance, got %d", distance)
				}
			} else {
				// Different content should produce different hashes
				assert.NotEqual(t, hash1, hash2, "Expected different content to produce different hashes")
			}
		})
	}
}

func TestGenerateShingles(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		n        int
		expected []string
	}{
		{
			name:     "basic 3-grams",
			text:     "hello",
			n:        3,
			expected: []string{"hel", "ell", "llo"},
		},
		{
			name:     "text shorter than n",
			text:     "hi",
			n:        3,
			expected: []string{"hi"},
		},
		{
			name:     "with spaces normalized",
			text:     "hello  world",
			n:        3,
			expected: []string{"hel", "ell", "llo", "lo ", "o w", " wo", "wor", "orl", "rld"},
		},
		{
			name:     "case normalization",
			text:     "HeLLo",
			n:        3,
			expected: []string{"hel", "ell", "llo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateShingles(tt.text, tt.n)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHashShingle(t *testing.T) {
	// Test that same shingle produces same hash
	shingle1 := "abc"
	shingle2 := "abc"
	shingle3 := "def"

	hash1 := hashShingle(shingle1)
	hash2 := hashShingle(shingle2)
	hash3 := hashShingle(shingle3)

	assert.Equal(t, hash1, hash2, "Same shingle should produce same hash")
	assert.NotEqual(t, hash1, hash3, "Different shingles should produce different hashes")
	assert.NotZero(t, hash1, "Hash should not be zero for non-empty shingle")
}

func TestRemoveDuplicateShingles(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "no duplicates",
			input:    []string{"abc", "def", "ghi"},
			expected: []string{"abc", "def", "ghi"},
		},
		{
			name:     "with duplicates",
			input:    []string{"abc", "def", "abc", "ghi", "def"},
			expected: []string{"abc", "def", "ghi"},
		},
		{
			name:     "empty input",
			input:    []string{},
			expected: []string{},
		},
		{
			name:     "all duplicates",
			input:    []string{"abc", "abc", "abc"},
			expected: []string{"abc"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := removeDuplicateShingles(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHammingDistance(t *testing.T) {
	tests := []struct {
		name     string
		hash1    uint64
		hash2    uint64
		expected int
	}{
		{"identical hashes", 0b1010101, 0b1010101, 0},
		{"one bit different", 0b1010101, 0b1010100, 1},
		{"two bits different", 0b1010101, 0b1000001, 2}, // Two bits differ
		{"completely different", 0b0000000, 0b1111111, 7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HammingDistance(tt.hash1, tt.hash2)
			if result != tt.expected {
				t.Errorf("HammingDistance() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestExtractFileExtension(t *testing.T) {
	tests := []struct {
		name     string
		urlPath  string
		expected string
	}{
		{"HTML file", "/path/page.html", ".html"},
		{"PHP file", "/path/page.php", ".php"},
		{"No extension", "/path/page", ""},
		{"Multiple dots", "/path/file.name.txt", ".txt"},
		{"Root path", "/", ""},
		{"Hidden file", "/path/.hidden", ""},
		{"Extension in directory", "/path.html/page", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractFileExtension(tt.urlPath)
			if result != tt.expected {
				t.Errorf("ExtractFileExtension() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestCountPathSegments(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected int
	}{
		{"root path", "/", 0},
		{"empty path", "", 0},
		{"single segment", "/page", 1},
		{"multiple segments", "/path/to/page", 3},
		{"trailing slash", "/path/to/page/", 3},
		{"leading slash", "path/to/page", 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CountPathSegments(tt.path)
			if result != tt.expected {
				t.Errorf("CountPathSegments() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// Benchmark tests
func BenchmarkURLNormalizer_Normalize(b *testing.B) {
	normalizer := NewURLNormalizer()
	url := "HTTP://EXAMPLE.COM/Path/../Page?z=3&a=1&b=2&utm_source=test#fragment"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		normalizer.Normalize(url)
	}
}

func BenchmarkURLProcessor_Process(b *testing.B) {
	processor := NewURLProcessor()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		url := "http://example.com/page" + string(rune(i))
		processor.Process(url)
	}
}

func BenchmarkCalculateSimhash(b *testing.B) {
	content := "This is a test document with some words that we want to hash using simhash algorithm for similarity detection"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CalculateSimhash(content)
	}
}
