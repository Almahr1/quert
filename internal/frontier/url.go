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
	mutex               sync.RWMutex
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
	mutex               sync.RWMutex
}

type URLDeduplicator struct {
	urlSeen        map[string]struct{} // Simple map for now, bloom filter later
	contentHashes  map[string]string   // SHA-256 hash -> URL
	simhashes      map[uint64]string   // Simhash -> URL
	semanticHashes map[string]string   // Embedding hash -> URL
	mutex          sync.RWMutex
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
	normalizer     *URLNormalizer
	validator      *URLValidator
	deduplicator   *URLDeduplicator
	processedCount uint64
	validCount     uint64
	duplicateCount uint64
	invalidCount   uint64
	mutex          sync.RWMutex
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
		mutex:               sync.RWMutex{},
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
		mutex:               sync.RWMutex{},
	}
}

func NewURLDeduplicator() *URLDeduplicator {
	return &URLDeduplicator{
		urlSeen:        make(map[string]struct{}),
		contentHashes:  make(map[string]string),
		simhashes:      make(map[uint64]string),
		semanticHashes: make(map[string]string),
		mutex:          sync.RWMutex{},
	}
}

func NewURLProcessor() *URLProcessor {
	return &URLProcessor{
		normalizer:     NewURLNormalizer(),
		validator:      NewURLValidator(),
		deduplicator:   NewURLDeduplicator(),
		processedCount: 0,
		validCount:     0,
		duplicateCount: 0,
		invalidCount:   0,
		mutex:          sync.RWMutex{},
	}
}

func (n *URLNormalizer) Normalize(rawURL string) (string, error) {
	n.mutex.RLock()
	defer n.mutex.RUnlock()

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
	q := u.Query()
	if len(q) > 0 {
		newQ := url.Values{}

		// Apply parameter filtering
		for k, values := range q {
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
				for _, allowed_param := range n.ParamWhitelist {
					if k == allowed_param {
						allowed = true
						break
					}
				}
				if !allowed {
					continue
				}
			}

			// Filter empty values if configured
			for _, v := range values {
				if n.RemoveEmptyParams && v == "" {
					continue
				}
				newQ.Add(k, v)
			}
		}

		// Sort parameters if configured
		if n.SortQueryParams && len(newQ) > 0 {
			keys := make([]string, 0, len(newQ))
			for k := range newQ {
				keys = append(keys, k)
			}
			sort.Strings(keys)

			sortedQ := url.Values{}
			for _, k := range keys {
				values := newQ[k]
				sort.Strings(values)
				for _, v := range values {
					sortedQ.Add(k, v)
				}
			}
			u.RawQuery = sortedQ.Encode()
		} else {
			u.RawQuery = newQ.Encode()
		}
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
	n.mutex.RLock()
	defer n.mutex.RUnlock()

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
	n.mutex.RLock()
	defer n.mutex.RUnlock()

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
	n.mutex.RLock()
	defer n.mutex.RUnlock()

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
	n.mutex.RLock()
	defer n.mutex.RUnlock()

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
	v.mutex.RLock()
	defer v.mutex.RUnlock()

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
	v.mutex.RLock()
	defer v.mutex.RUnlock()

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
	v.mutex.RLock()
	defer v.mutex.RUnlock()

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
	v.mutex.RLock()
	defer v.mutex.RUnlock()

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
	v.mutex.RLock()
	defer v.mutex.RUnlock()

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
	v.mutex.RLock()
	defer v.mutex.RUnlock()

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

func (v *URLValidator) ValidateLength(url string) bool {
	v.mutex.RLock()
	defer v.mutex.RUnlock()

	if v.MaxURLLength <= 0 {
		return true // No length restriction
	}

	return len(url) <= v.MaxURLLength
}

func (v *URLValidator) ValidateDepth(rawURL string) bool {
	v.mutex.RLock()
	defer v.mutex.RUnlock()

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
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	_, exists := d.urlSeen[url]
	return exists
}

func (d *URLDeduplicator) IsContentDuplicate(contentHash string) (bool, string) {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	if existingURL, exists := d.contentHashes[contentHash]; exists {
		return true, existingURL
	}
	return false, ""
}

func (d *URLDeduplicator) IsSimilar(simhash uint64, threshold int) (bool, string) {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	for existingHash, url := range d.simhashes {
		distance := HammingDistance(simhash, existingHash)
		if distance <= threshold {
			return true, url
		}
	}
	return false, ""
}

func (d *URLDeduplicator) IsSemanticallyDuplicate(embeddingHash string) (bool, string) {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	if existingURL, exists := d.semanticHashes[embeddingHash]; exists {
		return true, existingURL
	}
	return false, ""
}

func (d *URLDeduplicator) AddURL(url string) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	d.urlSeen[url] = struct{}{}
}

func (d *URLDeduplicator) AddContentHash(hash, url string) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	d.contentHashes[hash] = url
}

func (d *URLDeduplicator) AddSimhash(hash uint64, url string) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	d.simhashes[hash] = url
}

func (d *URLDeduplicator) AddSemanticHash(hash, url string) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	d.semanticHashes[hash] = url
}

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
	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.processedCount++

	// 1. Normalize URL
	normalizedURL, err := p.normalizer.Normalize(rawURL)
	if err != nil {
		p.invalidCount++
		return nil, fmt.Errorf("failed to normalize URL: %w", err)
	}

	// 2. Check for duplicates (Level 1: URL deduplication)
	if p.deduplicator.IsDuplicate(normalizedURL) {
		p.duplicateCount++
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
	if !p.validator.IsValid(urlInfo) {
		p.invalidCount++
		return nil, fmt.Errorf("URL validation failed: %s", normalizedURL)
	}

	// 6. Add to deduplication tracking
	p.deduplicator.AddURL(normalizedURL)
	p.validCount++

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
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	return map[string]uint64{
		"processed_count": p.processedCount,
		"valid_count":     p.validCount,
		"duplicate_count": p.duplicateCount,
		"invalid_count":   p.invalidCount,
	}
}

func (p *URLProcessor) Reset() {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Reset counters
	p.processedCount = 0
	p.validCount = 0
	p.duplicateCount = 0
	p.invalidCount = 0

	// Reset deduplicator
	p.deduplicator = NewURLDeduplicator()
}

func (p *URLProcessor) UpdateConfiguration(normalizer *URLNormalizer, validator *URLValidator) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if normalizer != nil {
		p.normalizer = normalizer
	}
	if validator != nil {
		p.validator = validator
	}
}

func CalculateContentHash(content []byte) string {
	hash := sha256.Sum256(content)
	return fmt.Sprintf("%x", hash)
}

func CalculateSimhash(content string) uint64 {
	// Simple simhash implementation for demonstration
	// In production, you'd want a more sophisticated implementation
	// using shingling and proper hash functions

	var hash uint64
	wordCounts := make(map[string]int)

	// Simple word tokenization
	words := strings.Fields(strings.ToLower(content))
	for _, word := range words {
		// Clean word (remove punctuation)
		cleaned := regexp.MustCompile(`[^a-zA-Z0-9]+`).ReplaceAllString(word, "")
		if len(cleaned) > 2 { // Skip very short words
			wordCounts[cleaned]++
		}
	}

	// Simple hash calculation based on word frequencies
	for word, count := range wordCounts {
		wordHash := sha256.Sum256([]byte(word))
		// Use first 8 bytes as uint64
		var wordHashUint64 uint64
		for i := 0; i < 8; i++ {
			wordHashUint64 = (wordHashUint64 << 8) | uint64(wordHash[i])
		}

		// Weight by frequency and XOR into final hash
		for i := 0; i < count && i < 10; i++ { // Cap influence of very frequent words
			hash ^= wordHashUint64
		}
	}

	return hash
}

func HammingDistance(hash1, hash2 uint64) int {
	// XOR the two hashes to find differing bits
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
