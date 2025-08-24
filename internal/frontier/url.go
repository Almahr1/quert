package frontier

import (
	"crypto/sha256"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type URLInfo struct {
	URL           string            `json:"url"`
	OriginalURL   string            `json:"original_url"`
	NormalizedURL string            `json:"normalized_url"`
	Domain        string            `json:"domain"`
	Subdomain     string            `json:"subdomain"`
	Priority      int               `json:"priority"`
	Depth         int               `json:"depth"`
	DiscoveredAt  time.Time         `json:"discovered_at"`
	LastCrawled   *time.Time        `json:"last_crawled,omitempty"`
	CrawlCount    int               `json:"crawl_count"`
	Metadata      map[string]string `json:"metadata"`
	ContentType   string            `json:"content_type,omitempty"`
	StatusCode    int               `json:"status_code,omitempty"`
}

type URLNormalizer struct {
	RemoveFragments     bool
	SortQueryParams     bool
	RemoveDefaultPorts  bool
	LowercaseHost       bool
	RemoveEmptyParams   bool
	DecodeUnreserved    bool
	RemoveTrailingSlash bool
	ParamWhitelist      []string
	ParamBlacklist      []string
	Mutex               sync.RWMutex
}

type URLValidator struct {
	AllowedSchemes      []string
	AllowedDomains      []string
	BlockedDomains      []string
	AllowedContentTypes []string
	BlockedContentTypes []string
	IncludePatterns     []string
	ExcludePatterns     []string
	AllowedExtensions   []string
	BlockedExtensions   []string
	MaxURLLength        int
	MaxPathDepth        int
	Mutex               sync.RWMutex
}

type URLDeduplicator struct {
	URLSeen        map[string]struct{} // Simple map for now, bloom filter later
	ContentHashes  map[string]string   // SHA-256 hash -> URL
	Simhashes      map[uint64]string   // Simhash -> URL
	SemanticHashes map[string]string   // Embedding hash -> URL
	Mutex          sync.RWMutex
}

type DomainInfo struct {
	Domain       string    `json:"domain"`
	Subdomain    string    `json:"subdomain"`
	TLD          string    `json:"tld"`
	IsIP         bool      `json:"is_ip"`
	FirstSeen    time.Time `json:"first_seen"`
	LastCrawled  time.Time `json:"last_crawled"`
	URLCount     int       `json:"url_count"`
	SuccessCount int       `json:"success_count"`
	ErrorCount   int       `json:"error_count"`
}

type URLProcessor struct {
	Normalizer     *URLNormalizer
	Validator      *URLValidator
	Deduplicator   *URLDeduplicator
	ProcessedCount uint64
	ValidCount     uint64
	DuplicateCount uint64
	InvalidCount   uint64
	Mutex          sync.RWMutex
}

func NewURLNormalizer() *URLNormalizer {
	return &URLNormalizer{
		RemoveFragments:     true,
		SortQueryParams:     true,
		RemoveDefaultPorts:  true,
		LowercaseHost:       true,
		RemoveEmptyParams:   true,
		DecodeUnreserved:    true,
		RemoveTrailingSlash: false,
		ParamWhitelist:      []string{},
		ParamBlacklist:      []string{"utm_source", "utm_medium", "utm_campaign", "utm_content", "utm_term", "fbclid", "gclid", "ref", "source"},
		Mutex:               sync.RWMutex{},
	}
}

func NewURLValidator() *URLValidator {
	return &URLValidator{
		AllowedSchemes:      []string{"http", "https"},
		AllowedDomains:      []string{},
		BlockedDomains:      []string{"localhost", "127.0.0.1", "0.0.0.0", "::1"},
		AllowedContentTypes: []string{"text/html", "text/plain", "application/xhtml+xml", "application/xml"},
		BlockedContentTypes: []string{"application/octet-stream", "application/pdf", "image/*", "video/*", "audio/*"},
		IncludePatterns:     []string{},
		ExcludePatterns:     []string{},
		AllowedExtensions:   []string{".html", ".htm", ".php", ".asp", ".aspx", ".jsp"},
		BlockedExtensions:   []string{".pdf", ".jpg", ".jpeg", ".png", ".gif", ".mp4", ".avi", ".zip", ".rar", ".exe", ".dmg"},
		MaxURLLength:        2048,
		MaxPathDepth:        10,
		Mutex:               sync.RWMutex{},
	}
}

func NewURLDeduplicator() *URLDeduplicator {
	return &URLDeduplicator{
		URLSeen:        make(map[string]struct{}),
		ContentHashes:  make(map[string]string),
		Simhashes:      make(map[uint64]string),
		SemanticHashes: make(map[string]string),
		Mutex:          sync.RWMutex{},
	}
}

func NewURLProcessor() *URLProcessor {
	return &URLProcessor{
		Normalizer:     NewURLNormalizer(),
		Validator:      NewURLValidator(),
		Deduplicator:   NewURLDeduplicator(),
		ProcessedCount: 0,
		ValidCount:     0,
		DuplicateCount: 0,
		InvalidCount:   0,
		Mutex:          sync.RWMutex{},
	}
}

func (n *URLNormalizer) Normalize(rawURL string) (string, error) {
	n.Mutex.RLock()
	defer n.Mutex.RUnlock()

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}

	// 1. Case normalization (lowercase host)
	if n.LowercaseHost {
		u.Host = strings.ToLower(u.Host)
	}

	// 2. Default port removal
	if n.RemoveDefaultPorts {
		if (u.Scheme == "http" && strings.HasSuffix(u.Host, ":80")) ||
			(u.Scheme == "https" && strings.HasSuffix(u.Host, ":443")) {
			hostParts := strings.Split(u.Host, ":")
			u.Host = hostParts[0]
		}
	}

	// 3. Path normalization (resolve . and ..)
	if u.Path != "" {
		u.Path = path.Clean(u.Path)
	}

	// 4. Query parameter processing
	if u.RawQuery != "" {
		u.RawQuery = n.processQueryParameters(u.RawQuery)
	}

	// 5. Fragment removal
	if n.RemoveFragments {
		u.Fragment = ""
	}

	// 6. Trailing slash handling
	if n.RemoveTrailingSlash && len(u.Path) > 1 && strings.HasSuffix(u.Path, "/") {
		u.Path = strings.TrimSuffix(u.Path, "/")
	}

	return u.String(), nil
}

func (n *URLNormalizer) NormalizeBatch(urls []string) ([]string, []error) {
	const numWorkers = 100

	results := make([]string, len(urls))
	errs := make([]error, len(urls))

	jobs := make(chan struct {
		index int
		url   string
	}, len(urls))
	var wg sync.WaitGroup

	// Start workers
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				normalizedURL, err := n.Normalize(job.url)
				results[job.index] = normalizedURL
				errs[job.index] = err
			}
		}()
	}

	// Send jobs
	for i, url := range urls {
		jobs <- struct {
			index int
			url   string
		}{i, url}
	}
	close(jobs)

	wg.Wait()
	return results, errs
}

func ParseURL(rawURL string) (*url.URL, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("empty URL")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL: %w", err)
	}

	if u.Scheme == "" {
		return nil, fmt.Errorf("URL missing scheme")
	}

	return u, nil
}

func (n *URLNormalizer) Canonicalize(parsedURL *url.URL) string {
	n.Mutex.RLock()
	defer n.Mutex.RUnlock()

	// Create a copy to avoid modifying the original
	u := *parsedURL

	// Apply host normalization
	if n.LowercaseHost {
		u.Host = strings.ToLower(u.Host)
	}

	// Remove default ports
	if n.RemoveDefaultPorts {
		u.Host = RemoveDefaultPort(u.Host, u.Scheme)
	}

	// Normalize path
	if u.Path != "" {
		u.Path = path.Clean(u.Path)
	}

	// Remove trailing slash if configured
	if n.RemoveTrailingSlash && len(u.Path) > 1 && strings.HasSuffix(u.Path, "/") {
		u.Path = strings.TrimSuffix(u.Path, "/")
	}

	// Sort query parameters if present
	if u.RawQuery != "" {
		u.RawQuery = SortQueryParams(u.RawQuery)
	}

	// Remove fragment if configured
	if n.RemoveFragments {
		u.Fragment = ""
	}

	return u.String()
}

func (n *URLNormalizer) NormalizeHost(host string) string {
	n.Mutex.RLock()
	defer n.Mutex.RUnlock()

	if host == "" {
		return host
	}

	// Normalize case
	if n.LowercaseHost {
		host = normalizeHost(host)
	}

	return host
}

func (n *URLNormalizer) NormalizePath(inputPath string) string {
	n.Mutex.RLock()
	defer n.Mutex.RUnlock()

	if inputPath == "" {
		return inputPath
	}

	// Clean path to resolve . and .. and remove double slashes
	cleanPath := path.Clean(inputPath)

	// Handle trailing slash based on configuration
	if n.RemoveTrailingSlash && len(cleanPath) > 1 && strings.HasSuffix(cleanPath, "/") {
		cleanPath = strings.TrimSuffix(cleanPath, "/")
	}

	return cleanPath
}

func (n *URLNormalizer) NormalizeQuery(query string) string {
	n.Mutex.RLock()
	defer n.Mutex.RUnlock()

	return n.processQueryParameters(query)
}

// processQueryParameters is a shared helper for query parameter processing
func (n *URLNormalizer) processQueryParameters(query string) string {
	if query == "" {
		return query
	}

	values, err := url.ParseQuery(query)
	if err != nil {
		return query // Return original if can't parse
	}

	newValues := url.Values{}

	// Apply parameter filtering
	for k, vals := range values {
		// Skip if in blacklist
		skip := false
		for _, blocked := range n.ParamBlacklist {
			if k == blocked {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		// Skip if whitelist exists and param not in it
		if len(n.ParamWhitelist) > 0 {
			allowed := false
			for _, allowedParam := range n.ParamWhitelist {
				if k == allowedParam {
					allowed = true
					break
				}
			}
			if !allowed {
				continue
			}
		}

		// Filter empty values if configured
		for _, v := range vals {
			if n.RemoveEmptyParams && v == "" {
				continue
			}
			newValues.Add(k, v)
		}
	}

	// Sort parameters if configured
	if n.SortQueryParams {
		return SortQueryParams(newValues.Encode())
	}

	return newValues.Encode()
}

func (v *URLValidator) IsValid(urlInfo *URLInfo) bool {
	v.Mutex.RLock()
	defer v.Mutex.RUnlock()

	if urlInfo == nil || urlInfo.URL == "" {
		return false
	}

	u, err := url.Parse(urlInfo.URL)
	if err != nil {
		return false
	}

	// 1. Scheme validation
	if !v.ValidateScheme(u.Scheme) {
		return false
	}

	// 2. Domain filtering
	if !v.ValidateDomain(u.Host) {
		return false
	}

	// 3. Content type filtering
	if urlInfo.ContentType != "" && !v.ValidateContentType(urlInfo.ContentType) {
		return false
	}

	// 4. Pattern matching
	if !v.ValidatePatterns(urlInfo.URL) {
		return false
	}

	// 5. Extension filtering
	if !v.ValidateExtension(urlInfo.URL) {
		return false
	}

	// 6. Length and depth limits
	if !v.ValidateLength(urlInfo.URL) || !v.ValidateDepth(urlInfo.URL) {
		return false
	}

	return true
}

func (v *URLValidator) ValidateScheme(scheme string) bool {
	v.Mutex.RLock()
	defer v.Mutex.RUnlock()

	if len(v.AllowedSchemes) == 0 {
		return true // No restrictions
	}

	for _, allowed := range v.AllowedSchemes {
		if strings.EqualFold(scheme, allowed) {
			return true
		}
	}

	return false
}

func (v *URLValidator) ValidateDomain(domain string) bool {
	v.Mutex.RLock()
	defer v.Mutex.RUnlock()

	if domain == "" {
		return false
	}

	// Remove port if present
	host := domain
	if strings.Contains(host, ":") {
		hostParts := strings.Split(host, ":")
		host = hostParts[0]
	}

	// Check blocked domains first
	for _, blocked := range v.BlockedDomains {
		if strings.EqualFold(host, blocked) || strings.HasSuffix(strings.ToLower(host), "."+strings.ToLower(blocked)) {
			return false
		}
	}

	// If no allowed domains specified, allow all (except blocked)
	if len(v.AllowedDomains) == 0 {
		return true
	}

	// Check if domain matches allowed list
	for _, allowed := range v.AllowedDomains {
		if strings.EqualFold(host, allowed) || strings.HasSuffix(strings.ToLower(host), "."+strings.ToLower(allowed)) {
			return true
		}
	}

	return false
}

func (v *URLValidator) ValidateContentType(contentType string) bool {
	v.Mutex.RLock()
	defer v.Mutex.RUnlock()

	if contentType == "" {
		return true // No content type to validate
	}

	// Extract main type (before semicolon)
	mainType := strings.Split(contentType, ";")[0]
	mainType = strings.TrimSpace(strings.ToLower(mainType))

	// Check blocked content types first
	for _, blocked := range v.BlockedContentTypes {
		if strings.Contains(blocked, "*") {
			// Handle wildcard matching (e.g., "image/*")
			pattern := strings.Replace(blocked, "*", "", -1)
			if strings.HasPrefix(mainType, pattern) {
				return false
			}
		} else if strings.EqualFold(mainType, blocked) {
			return false
		}
	}

	// If no allowed content types specified, allow all (except blocked)
	if len(v.AllowedContentTypes) == 0 {
		return true
	}

	// Check if content type matches allowed list
	for _, allowed := range v.AllowedContentTypes {
		if strings.Contains(allowed, "*") {
			// Handle wildcard matching
			pattern := strings.Replace(allowed, "*", "", -1)
			if strings.HasPrefix(mainType, pattern) {
				return true
			}
		} else if strings.EqualFold(mainType, allowed) {
			return true
		}
	}

	return false
}

func (v *URLValidator) ValidatePatterns(url string) bool {
	v.Mutex.RLock()
	defer v.Mutex.RUnlock()

	// Check exclude patterns first
	for _, pattern := range v.ExcludePatterns {
		if matched, _ := regexp.MatchString(pattern, url); matched {
			return false
		}
	}

	// If no include patterns specified, allow all (except excluded)
	if len(v.IncludePatterns) == 0 {
		return true
	}

	// Check if URL matches any include pattern
	for _, pattern := range v.IncludePatterns {
		if matched, _ := regexp.MatchString(pattern, url); matched {
			return true
		}
	}

	return false
}

func (v *URLValidator) ValidateExtension(rawURL string) bool {
	v.Mutex.RLock()
	defer v.Mutex.RUnlock()

	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	ext := ExtractFileExtension(u.Path)
	if ext == "" {
		return true // No extension, let other validators handle
	}

	// Check blocked extensions first
	for _, blocked := range v.BlockedExtensions {
		if strings.EqualFold(ext, blocked) {
			return false
		}
	}

	// If no allowed extensions specified, allow all (except blocked)
	if len(v.AllowedExtensions) == 0 {
		return true
	}

	// Check if extension matches allowed list
	for _, allowed := range v.AllowedExtensions {
		if strings.EqualFold(ext, allowed) {
			return true
		}
	}

	return false
}

func (v *URLValidator) ValidateLength(rawURL string) bool {
	v.Mutex.RLock()
	defer v.Mutex.RUnlock()

	if v.MaxURLLength <= 0 {
		return true // No length restriction
	}

	return len(rawURL) <= v.MaxURLLength
}

func (v *URLValidator) ValidateDepth(rawURL string) bool {
	v.Mutex.RLock()
	defer v.Mutex.RUnlock()

	if v.MaxPathDepth <= 0 {
		return true // No depth restriction
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	depth := CountPathSegments(u.Path)
	return depth <= v.MaxPathDepth
}

func (d *URLDeduplicator) IsDuplicate(url string) bool {
	d.Mutex.RLock()
	defer d.Mutex.RUnlock()

	_, exists := d.URLSeen[url]
	return exists
}

func (d *URLDeduplicator) IsContentDuplicate(contentHash string) (bool, string) {
	d.Mutex.RLock()
	defer d.Mutex.RUnlock()

	if existingURL, exists := d.ContentHashes[contentHash]; exists {
		return true, existingURL
	}
	return false, ""
}

func (d *URLDeduplicator) IsSimilar(simhash uint64, threshold int) (bool, string) {
	d.Mutex.RLock()
	defer d.Mutex.RUnlock()

	for existingHash, url := range d.Simhashes {
		distance := HammingDistance(simhash, existingHash)
		if distance <= threshold {
			return true, url
		}
	}
	return false, ""
}

func (d *URLDeduplicator) IsSemanticallyDuplicate(embeddingHash string) (bool, string) {
	d.Mutex.RLock()
	defer d.Mutex.RUnlock()

	if existingURL, exists := d.SemanticHashes[embeddingHash]; exists {
		return true, existingURL
	}
	return false, ""
}

func (d *URLDeduplicator) AddURL(url string) {
	d.Mutex.Lock()
	defer d.Mutex.Unlock()

	d.URLSeen[url] = struct{}{}
}

func (d *URLDeduplicator) AddContentHash(hash, url string) {
	d.Mutex.Lock()
	defer d.Mutex.Unlock()

	d.ContentHashes[hash] = url
}

func (d *URLDeduplicator) AddSimhash(hash uint64, url string) {
	d.Mutex.Lock()
	defer d.Mutex.Unlock()

	d.Simhashes[hash] = url
}

func (d *URLDeduplicator) AddSemanticHash(hash, url string) {
	d.Mutex.Lock()
	defer d.Mutex.Unlock()

	d.SemanticHashes[hash] = url
}

// ExtractHostFromURL extracts the host (domain:port) from a URL string.
// Returns the full host including port if present (e.g., "example.com:8080").
// Automatically adds "http://" scheme if missing.
func ExtractHostFromURL(rawURL string) (string, error) {
	if !strings.Contains(rawURL, "://") {
		rawURL = "http://" + rawURL
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}

	host := u.Host
	if host == "" {
		return "", fmt.Errorf("no host found in URL")
	}

	return host, nil
}

// ExtractDomain extracts just the domain name from a URL string.
// Returns the domain without port (e.g., "example.com" from "https://example.com:8080/path").
// Requires a properly formatted URL with scheme.
func ExtractDomain(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse URL: %w", err)
	}

	host := u.Host
	if host == "" {
		return "", fmt.Errorf("URL has no host")
	}

	// Remove port if present
	if strings.Contains(host, ":") {
		hostParts := strings.Split(host, ":")
		host = hostParts[0]
	}

	return host, nil
}

func ExtractSubdomain(rawURL string) (string, error) {
	domain, err := ExtractDomain(rawURL)
	if err != nil {
		return "", err
	}

	// Split domain into parts
	parts := strings.Split(domain, ".")
	if len(parts) < 3 {
		return "", nil // No subdomain (e.g., "example.com")
	}

	// Everything except the last two parts is considered subdomain
	// e.g., "www.blog.example.com" -> "www.blog"
	subdomainParts := parts[:len(parts)-2]
	return strings.Join(subdomainParts, "."), nil
}

func ExtractTLD(rawURL string) (string, error) {
	domain, err := ExtractDomain(rawURL)
	if err != nil {
		return "", err
	}

	// Simple TLD extraction (last part after last dot)
	parts := strings.Split(domain, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid domain format")
	}

	// Return the last part as TLD
	return parts[len(parts)-1], nil
}

func GetDomainInfo(rawURL string) (*DomainInfo, error) {
	domain, err := ExtractDomain(rawURL)
	if err != nil {
		return nil, err
	}

	subdomain, _ := ExtractSubdomain(rawURL)
	tld, _ := ExtractTLD(rawURL)
	isIP := IsIPAddress(domain)

	return &DomainInfo{
		Domain:       domain,
		Subdomain:    subdomain,
		TLD:          tld,
		IsIP:         isIP,
		FirstSeen:    time.Now(),
		LastCrawled:  time.Time{}, // Will be set when crawled
		URLCount:     0,
		SuccessCount: 0,
		ErrorCount:   0,
	}, nil
}

func IsValidDomain(domain string) bool {
	if domain == "" {
		return false
	}

	// Basic domain validation using regex
	// Matches basic domain format: alphanumeric, dots, hyphens
	domainRegex := regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.([a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?))*$`)
	return domainRegex.MatchString(domain)
}

func IsIPAddress(host string) bool {
	if host == "" {
		return false
	}

	// Try parsing as IPv4 or IPv6
	ip := net.ParseIP(host)
	return ip != nil
}

func (p *URLProcessor) Process(rawURL string) (*URLInfo, error) {
	p.Mutex.Lock()
	defer p.Mutex.Unlock()

	p.ProcessedCount++

	// 1. Normalize URL
	normalizedURL, err := p.Normalizer.Normalize(rawURL)
	if err != nil {
		p.InvalidCount++
		return nil, fmt.Errorf("failed to normalize URL: %w", err)
	}

	// 2. Check for duplicates (Level 1: URL deduplication)
	if p.Deduplicator.IsDuplicate(normalizedURL) {
		p.DuplicateCount++
		return nil, fmt.Errorf("URL already processed: %s", normalizedURL)
	}

	// 3. Extract domain information
	domain, _ := ExtractDomain(normalizedURL)
	subdomain, _ := ExtractSubdomain(normalizedURL)

	// 4. Create URLInfo structure
	urlInfo := &URLInfo{
		URL:           normalizedURL,
		OriginalURL:   rawURL,
		NormalizedURL: normalizedURL,
		Domain:        domain,
		Subdomain:     subdomain,
		Priority:      0, // Default priority
		Depth:         0, // Will be set by crawler based on discovery path
		DiscoveredAt:  time.Now(),
		LastCrawled:   nil,
		CrawlCount:    0,
		Metadata:      make(map[string]string),
	}

	// 5. Validate URL
	if !p.Validator.IsValid(urlInfo) {
		p.InvalidCount++
		return nil, fmt.Errorf("URL validation failed: %s", normalizedURL)
	}

	// 6. Add to deduplication tracking
	p.Deduplicator.AddURL(normalizedURL)
	p.ValidCount++

	return urlInfo, nil
}

func (p *URLProcessor) ProcessBatch(urls []string) ([]*URLInfo, []error) {
	const numWorkers = 50

	results := make([]*URLInfo, len(urls))
	errors := make([]error, len(urls))

	jobs := make(chan struct {
		index int
		url   string
	}, len(urls))
	var wg sync.WaitGroup

	// Start workers
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				urlInfo, err := p.Process(job.url)
				results[job.index] = urlInfo
				errors[job.index] = err
			}
		}()
	}

	// Send jobs
	for i, url := range urls {
		jobs <- struct {
			index int
			url   string
		}{i, url}
	}
	close(jobs)

	wg.Wait()
	return results, errors
}

func (p *URLProcessor) GetStatistics() map[string]uint64 {
	p.Mutex.RLock()
	defer p.Mutex.RUnlock()

	return map[string]uint64{
		"processed_count": p.ProcessedCount,
		"valid_count":     p.ValidCount,
		"duplicate_count": p.DuplicateCount,
		"invalid_count":   p.InvalidCount,
	}
}

func (p *URLProcessor) Reset() {
	p.Mutex.Lock()
	defer p.Mutex.Unlock()

	// Reset counters
	p.ProcessedCount = 0
	p.ValidCount = 0
	p.DuplicateCount = 0
	p.InvalidCount = 0

	// Reset deduplicator
	p.Deduplicator = NewURLDeduplicator()
}

func (p *URLProcessor) UpdateConfiguration(normalizer *URLNormalizer, validator *URLValidator) {
	p.Mutex.Lock()
	defer p.Mutex.Unlock()

	if normalizer != nil {
		p.Normalizer = normalizer
	}
	if validator != nil {
		p.Validator = validator
	}
}

func CalculateContentHash(content []byte) string {
	hash := sha256.Sum256(content)
	return fmt.Sprintf("%x", hash)
}

func CalculateSimhash(content string) uint64 {
	if content == "" {
		return 0
	}

	// Generate shingles (n-grams) from the content
	shingles := generateShingles(content, 3) // 3-grams

	// Initialize feature vector for 64-bit simhash
	var features [64]int

	// Process each shingle
	for _, shingle := range shingles {
		// Hash the shingle to get a 64-bit hash
		hash := hashShingle(shingle)

		// For each bit in the hash, update the feature vector
		for i := 0; i < 64; i++ {
			bit := (hash >> i) & 1
			if bit == 1 {
				features[i]++
			} else {
				features[i]--
			}
		}
	}

	// Convert feature vector to final simhash
	var simhash uint64
	for i := 0; i < 64; i++ {
		if features[i] > 0 {
			simhash |= (1 << i)
		}
	}

	return simhash
}

// generateShingles creates n-grams from the input text
func generateShingles(text string, n int) []string {
	if len(text) < n {
		return []string{text}
	}

	// Normalize text: lowercase, remove extra whitespace
	text = strings.ToLower(text)
	text = strings.Join(strings.Fields(text), " ")

	// Generate character-level n-grams
	var shingles []string
	runes := []rune(text)

	for i := 0; i <= len(runes)-n; i++ {
		shingle := string(runes[i : i+n])
		shingles = append(shingles, shingle)
	}

	// Remove duplicates to avoid bias
	return removeDuplicateShingles(shingles)
}

// hashShingle creates a 64-bit hash for a shingle using FNV-1a
func hashShingle(shingle string) uint64 {
	const (
		fnvOffsetBasis uint64 = 14695981039346656037
		fnvPrime       uint64 = 1099511628211
	)

	hash := fnvOffsetBasis
	for _, b := range []byte(shingle) {
		hash ^= uint64(b)
		hash *= fnvPrime
	}

	return hash
}

// removeDuplicateShingles removes duplicate shingles while preserving order
func removeDuplicateShingles(shingles []string) []string {
	if len(shingles) == 0 {
		return []string{}
	}

	seen := make(map[string]bool)
	var unique []string

	for _, shingle := range shingles {
		if !seen[shingle] {
			seen[shingle] = true
			unique = append(unique, shingle)
		}
	}

	return unique
}

func HammingDistance(hash1, hash2 uint64) int {
	xor := hash1 ^ hash2

	// Count the number of 1 bits (differing bits)
	count := 0
	for xor != 0 {
		count += int(xor & 1)
		xor >>= 1
	}

	return count
}

func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}

func RemoveDefaultPort(host, scheme string) string {
	if (scheme == "http" && strings.HasSuffix(host, ":80")) ||
		(scheme == "https" && strings.HasSuffix(host, ":443")) {
		hostParts := strings.Split(host, ":")
		return hostParts[0]
	}
	return host
}

func SortQueryParams(query string) string {
	if query == "" {
		return ""
	}

	values, err := url.ParseQuery(query)
	if err != nil {
		return query
	}

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	sortedValues := url.Values{}
	for _, k := range keys {
		paramValues := values[k]
		sort.Strings(paramValues)
		for _, v := range paramValues {
			sortedValues.Add(k, v)
		}
	}

	return sortedValues.Encode()
}

func ResolvePath(inputPath string) string {
	if inputPath == "" {
		return "/"
	}

	// Use path.Clean to resolve . and .. components
	// and remove redundant separators
	resolved := path.Clean(inputPath)

	// Ensure path starts with /
	if !strings.HasPrefix(resolved, "/") {
		resolved = "/" + resolved
	}

	return resolved
}

func ExtractFileExtension(urlPath string) string {
	lastSlash := strings.LastIndex(urlPath, "/")
	filename := urlPath
	if lastSlash != -1 {
		filename = urlPath[lastSlash+1:]
	}

	lastDot := strings.LastIndex(filename, ".")
	if lastDot == -1 || lastDot == 0 {
		return ""
	}

	return strings.ToLower(filename[lastDot:])
}

func CountPathSegments(path string) int {
	if path == "" || path == "/" {
		return 0
	}

	path = strings.Trim(path, "/")
	if path == "" {
		return 0
	}

	return len(strings.Split(path, "/"))
}
