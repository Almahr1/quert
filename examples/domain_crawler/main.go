package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Almahr1/quert/internal/config"
	"github.com/Almahr1/quert/internal/crawler"
	"github.com/Almahr1/quert/internal/frontier"
	"go.uber.org/zap"
)

// DomainCrawler demonstrates systematic crawling of specific domains
func main() {
	// Create logger
	logger, err := zap.NewDevelopment()
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Sync()

	// Target domains - choose reputable sites that allow crawling
	targetDomains := []string{
		"quotes.toscrape.com",  // Scraping practice site
		"httpbin.org",          // HTTP testing service
		"example.com",          // RFC example domain
		"news.ycombinator.com", // Hacker News (check robots.txt first!)
	}

	logger.Info("Starting systematic domain crawler",
		zap.Strings("target_domains", targetDomains),
		zap.Int("target_links", 1000))

	// Ask user which domain to focus on
	fmt.Printf("Available domains:\n")
	for i, domain := range targetDomains {
		fmt.Printf("%d. %s\n", i+1, domain)
	}
	fmt.Print("Select domain to crawl (1-4, or 0 for all): ")

	var choice int
	fmt.Scanf("%d", &choice)

	var selectedDomains []string
	if choice == 0 {
		selectedDomains = targetDomains
	} else if choice >= 1 && choice <= len(targetDomains) {
		selectedDomains = []string{targetDomains[choice-1]}
	} else {
		log.Fatal("Invalid choice")
	}

	// Build seed URLs
	var seedURLs []string
	for _, domain := range selectedDomains {
		seedURLs = append(seedURLs, "https://"+domain)
		// Add some common starting points
		seedURLs = append(seedURLs, "https://"+domain+"/")
		if domain == "quotes.toscrape.com" {
			seedURLs = append(seedURLs, "https://"+domain+"/page/1/")
		}
		if domain == "news.ycombinator.com" {
			seedURLs = append(seedURLs, "https://"+domain+"/newest")
		}
	}

	// Configure crawler for domain-focused crawling
	crawlerConfig := &config.CrawlerConfig{
		MaxPages:          500, // Increase for domain crawling
		MaxDepth:          4,   // Go deeper within domain
		ConcurrentWorkers: 50,  // Still be respectful
		RequestTimeout:    30 * time.Second,
		UserAgent:         "Quert-DomainCrawler/1.0 (+https://github.com/Almahr1/quert) Research",
		SeedURLs:          seedURLs,
		AllowedDomains:    selectedDomains, // Restrict to selected domains
		IncludePatterns: []string{
			".*\\.(html|htm|php|asp|aspx)$",
			".*/$",
			".*/page/.*", // Common pagination pattern
		},
		ExcludePatterns: []string{
			".*\\.(jpg|jpeg|png|gif|svg|ico|css|js|pdf|doc|zip)$",
			".*/admin/.*",
			".*/login.*",
			".*/logout.*",
			".*/search\\?.*", // Avoid search result pages
			".*/tag/.*",      // Avoid tag pages (can be numerous)
		},
	}

	httpConfig := &config.HTTPConfig{
		MaxIdleConnections:        15,
		MaxIdleConnectionsPerHost: 3,
		IdleConnectionTimeout:     45 * time.Second,
		DisableKeepAlives:         false,
		Timeout:                   30 * time.Second,
		DialTimeout:               10 * time.Second,
		TlsHandshakeTimeout:       15 * time.Second,
		ResponseHeaderTimeout:     15 * time.Second,
		DisableCompression:        false,
	}

	// Create enhanced output files
	timestamp := time.Now().Format("20060102_150405")
	linksFile := fmt.Sprintf("links_%s.txt", timestamp)
	statsFile := fmt.Sprintf("crawl_stats_%s.txt", timestamp)
	contentFile := fmt.Sprintf("content_samples_%s.txt", timestamp)

	// Create crawler
	engine := crawler.NewCrawlerEngine(crawlerConfig, httpConfig, nil, logger)

	// Lower quality threshold for more comprehensive crawling
	if engine.ExtractorFactory != nil {
		engine.ExtractorFactory.Config.QualityThreshold = 0.15
		engine.ExtractorFactory.Config.ExtractLinks = true
		engine.ExtractorFactory.Config.ExtractMetadata = true
		engine.ExtractorFactory.Config.MinTextLength = 30
	}

	// Advanced link collection with statistics
	linkData := make(map[string]LinkInfo)
	domainStats := make(map[string]*DomainStats)
	var dataMutex sync.RWMutex

	// Initialize domain stats
	for _, domain := range selectedDomains {
		domainStats[domain] = &DomainStats{
			Domain:       domain,
			LinksFound:   0,
			PagesVisited: 0,
			Errors:       0,
		}
	}

	// Start crawler
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute) // 15 minute limit
	defer cancel()

	if err := engine.Start(ctx); err != nil {
		logger.Fatal("Failed to start crawler", zap.Error(err))
	}

	// Enhanced result processing
	go processResults(engine, linkData, domainStats, &dataMutex, logger, cancel)

	// Submit seed URLs with staggered timing
	logger.Info("Submitting seed URLs...", zap.Int("count", len(seedURLs)))
	for i, seedURL := range seedURLs {
		job := &crawler.CrawlJob{
			URL:       seedURL,
			Priority:  1,
			Depth:     0,
			RequestID: fmt.Sprintf("seed-%d", i),
			Context:   ctx,
		}

		if err := engine.SubmitJob(job); err != nil {
			logger.Error("Failed to submit seed job", zap.String("url", seedURL), zap.Error(err))
		}

		// Respectful delay between submissions
		time.Sleep(1 * time.Second)
	}

	// Wait for completion
	<-ctx.Done()

	logger.Info("Shutting down crawler...")
	if err := engine.Stop(); err != nil {
		logger.Error("Error stopping crawler", zap.Error(err))
	}

	// Save results
	if err := saveResults(linkData, domainStats, engine, linksFile, statsFile, contentFile, logger); err != nil {
		logger.Error("Failed to save results", zap.Error(err))
	}

	logger.Info("Domain crawling completed!",
		zap.String("links_file", linksFile),
		zap.String("stats_file", statsFile),
		zap.String("content_file", contentFile))
}

// LinkInfo holds detailed information about collected links
type LinkInfo struct {
	URL         string
	Text        string
	SourceURL   string
	Domain      string
	Internal    bool
	FirstSeen   time.Time
	Frequency   int
	ContentType string
}

// DomainStats tracks statistics per domain
type DomainStats struct {
	Domain        string
	LinksFound    int
	PagesVisited  int
	Errors        int
	PageTitles    []string
	ResponseTimes []time.Duration
}

// processResults handles crawler results with enhanced data collection
func processResults(engine *crawler.CrawlerEngine, linkData map[string]LinkInfo,
	domainStats map[string]*DomainStats, dataMutex *sync.RWMutex,
	logger *zap.Logger, cancel context.CancelFunc) {

	results := engine.GetResults()
	for result := range results {
		if result == nil {
			continue
		}

		// Extract domain from URL
		domain, _ := frontier.ExtractDomain(result.URL)

		dataMutex.Lock()
		stats, exists := domainStats[domain]
		if !exists {
			stats = &DomainStats{Domain: domain}
			domainStats[domain] = stats
		}
		dataMutex.Unlock()

		if result.Success && result.ExtractedContent != nil {
			content := result.ExtractedContent

			dataMutex.Lock()
			stats.PagesVisited++
			stats.PageTitles = append(stats.PageTitles, content.Title)
			stats.ResponseTimes = append(stats.ResponseTimes, result.ResponseTime)

			// Process each link found
			for _, link := range content.Links {
				linkURL := link.URL

				// Update or create link info
				info, exists := linkData[linkURL]
				if !exists {
					info = LinkInfo{
						URL:         linkURL,
						Text:        link.Text,
						SourceURL:   result.URL,
						Internal:    link.Internal,
						FirstSeen:   time.Now(),
						Frequency:   1,
						ContentType: result.ContentType,
					}

					linkDomain, _ := frontier.ExtractDomain(linkURL)
					info.Domain = linkDomain
				} else {
					info.Frequency++
				}

				linkData[linkURL] = info
			}

			stats.LinksFound += len(content.Links)
			totalLinks := len(linkData)
			dataMutex.Unlock()

			logger.Info("Processed page",
				zap.String("url", result.URL),
				zap.String("domain", domain),
				zap.String("title", content.Title),
				zap.Int("links_on_page", len(content.Links)),
				zap.Int("total_unique_links", totalLinks),
				zap.Int("pages_visited", stats.PagesVisited))

			// Add internal links to crawl queue
			for _, link := range content.Links {
				if link.Internal && result.Job.Depth < 4 {
					job := &crawler.CrawlJob{
						URL:       link.URL,
						Priority:  2,
						Depth:     result.Job.Depth + 1,
						RequestID: fmt.Sprintf("internal-%d", time.Now().UnixNano()),
						Context:   result.Job.Context,
					}

					engine.SubmitJob(job) // Will be filtered by crawler if already processed
				}
			}

			// Check if we've reached target
			if totalLinks >= 1000 {
				logger.Info("Target reached!", zap.Int("total_links", totalLinks))
				cancel()
				return
			}

		} else if result.Error != nil {
			dataMutex.Lock()
			stats.Errors++
			dataMutex.Unlock()

			logger.Warn("Page failed",
				zap.String("url", result.URL),
				zap.String("domain", domain),
				zap.Error(result.Error))
		}
	}
}

// saveResults saves all collected data to files
func saveResults(linkData map[string]LinkInfo, domainStats map[string]*DomainStats,
	engine *crawler.CrawlerEngine, linksFile, statsFile, contentFile string,
	logger *zap.Logger) error {

	// Save links
	file, err := os.Create(linksFile)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	defer writer.Flush()

	// Write header
	fmt.Fprintln(writer, "URL\tDomain\tText\tSource\tInternal\tFrequency\tFirstSeen")

	// Sort links by frequency (most linked to first)
	var sortedLinks []LinkInfo
	for _, info := range linkData {
		sortedLinks = append(sortedLinks, info)
	}

	sort.Slice(sortedLinks, func(i, j int) bool {
		return sortedLinks[i].Frequency > sortedLinks[j].Frequency
	})

	// Write links
	for _, info := range sortedLinks {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%t\t%d\t%s\n",
			info.URL,
			info.Domain,
			strings.ReplaceAll(info.Text, "\n", " "),
			info.SourceURL,
			info.Internal,
			info.Frequency,
			info.FirstSeen.Format(time.RFC3339))
	}

	// Save statistics
	statsF, err := os.Create(statsFile)
	if err != nil {
		return err
	}
	defer statsF.Close()

	fmt.Fprintf(statsF, "CRAWL STATISTICS\n")
	fmt.Fprintf(statsF, "================\n\n")
	fmt.Fprintf(statsF, "Total Unique Links: %d\n", len(linkData))
	fmt.Fprintf(statsF, "Crawl Time: %s\n\n", time.Now().Format(time.RFC3339))

	// Domain statistics
	fmt.Fprintf(statsF, "DOMAIN BREAKDOWN:\n")
	for _, stats := range domainStats {
		if stats.PagesVisited > 0 {
			fmt.Fprintf(statsF, "\nDomain: %s\n", stats.Domain)
			fmt.Fprintf(statsF, "  Pages Visited: %d\n", stats.PagesVisited)
			fmt.Fprintf(statsF, "  Links Found: %d\n", stats.LinksFound)
			fmt.Fprintf(statsF, "  Errors: %d\n", stats.Errors)

			if len(stats.ResponseTimes) > 0 {
				var total time.Duration
				for _, rt := range stats.ResponseTimes {
					total += rt
				}
				avg := total / time.Duration(len(stats.ResponseTimes))
				fmt.Fprintf(statsF, "  Avg Response Time: %s\n", avg)
			}
		}
	}

	// Crawler metrics
	if metrics := engine.GetMetrics(); metrics != nil {
		fmt.Fprintf(statsF, "\nCRAWLER METRICS:\n")
		fmt.Fprintf(statsF, "  Total Jobs: %d\n", metrics.TotalJobs)
		fmt.Fprintf(statsF, "  Successful: %d\n", metrics.SuccessfulJobs)
		fmt.Fprintf(statsF, "  Failed: %d\n", metrics.FailedJobs)
		fmt.Fprintf(statsF, "  Jobs/Second: %.2f\n", metrics.JobsPerSecond)
		fmt.Fprintf(statsF, "  Uptime: %s\n", metrics.Uptime)
		fmt.Fprintf(statsF, "  Error Rate: %.2f%%\n", metrics.ErrorRate)
	}

	logger.Info("Results saved successfully",
		zap.String("links_file", linksFile),
		zap.String("stats_file", statsFile),
		zap.Int("total_links", len(linkData)))

	return nil
}
