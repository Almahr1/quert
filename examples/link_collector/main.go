package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Almahr1/quert/internal/config"
	"github.com/Almahr1/quert/internal/crawler"
	"github.com/Almahr1/quert/internal/frontier"
	"go.uber.org/zap"
)

func main() {
	logger, err := zap.NewDevelopment()
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Sync()

	crawlerConfig := &config.CrawlerConfig{
		MaxPages:          1000,
		MaxDepth:          1,
		ConcurrentWorkers: 100,
		RequestTimeout:    30 * time.Second,
		UserAgent:         "Quert-LinkCollector/1.0 (+https://github.com/Almahr1/quert) Educational Use",
		GlobalRateLimit:   10.0,
		GlobalBurst:       15,
		PerHostRateLimit:  5.0,
		PerHostBurst:      10,
		SeedURLs: []string{
			"https://lobste.rs",
			"https://reddit.com/r/programming",
			"https://reddit.com/r/webdev",
			"https://reddit.com/r/coding",
			"https://dev.to",

			"https://awesome.re",
			"https://github.com/sindresorhus/awesome",
			"https://github.com/topics",
			"https://github.com/trending",
			"https://github.com/collections",
			"https://bestofjs.org",

			"https://developer.mozilla.org/en-US/docs/Web",
			"https://www.w3.org/standards/",
			"https://stackoverflow.com/questions",
			"https://stackoverflow.com/tags",
			"https://devdocs.io",
			"https://roadmap.sh",

			"https://docs.python.org",
			"https://golang.org/doc/",
			"https://reactjs.org/docs/",
			"https://nodejs.org/en/docs/",
			"https://vuejs.org/guide/",
			"https://angular.io/docs",
			"https://developer.apple.com/documentation/",

			"https://opensource.com",
			"https://freecodecamp.org/news",
			"https://github.com/explore",
			"https://gitlab.com/explore",
			"https://sourceforge.net/directory/",
			"https://fosshub.com",

			"https://medium.com/tag/programming",
			"https://hashnode.com",
			"https://techcrunch.com",
			"https://arstechnica.com",
			"https://theverge.com",
			"https://wired.com",

			"https://leetcode.com",
			"https://codewars.com",
			"https://hackerrank.com",
			"https://exercism.org",
			"https://codingame.com",
			"https://topcoder.com",

			"https://httpbin.org/links/20",
			"https://quotes.toscrape.com",
			"https://books.toscrape.com",
			"https://example.com",

			"https://en.wikipedia.org/wiki/List_of_programming_languages",
			"https://en.wikipedia.org/wiki/Computer_science",
			"https://en.wikipedia.org/wiki/Software_engineering",
			"https://en.wikipedia.org/wiki/Web_development",
			"https://en.wikipedia.org/wiki/Machine_learning",
			"https://en.wikipedia.org/wiki/Artificial_intelligence",

			"https://npmjs.com",
			"https://pypi.org",
			"https://rubygems.org",
			"https://crates.io",
			"https://packagist.org",
			"https://nuget.org",

			"https://discourse.org",
			"https://spectrum.chat",
			"https://gitter.im",
			"https://discord.com/developers",
		},
		IncludePatterns: []string{
			".*\\.(html|htm|php|asp|aspx)$",
			".*/$",
		},
		ExcludePatterns: []string{
			".*\\.(jpg|jpeg|png|gif|svg|ico|css|js|pdf|doc|docx|zip|exe)$",
			".*/admin/.*",
			".*/wp-admin/.*",
			".*/login.*",
			".*/register.*",
		},
	}

	httpConfig := &config.HTTPConfig{
		MaxIdleConnections:        50,
		MaxIdleConnectionsPerHost: 10,
		IdleConnectionTimeout:     30 * time.Second,
		DisableKeepAlives:         false,
		Timeout:                   30 * time.Second,
		DialTimeout:               10 * time.Second,
		TlsHandshakeTimeout:       15 * time.Second,
		ResponseHeaderTimeout:     15 * time.Second,
		DisableCompression:        false,
	}

	engine := crawler.NewCrawlerEngine(crawlerConfig, httpConfig, nil, logger)

	// Configure extractor for better link collection
	// Lower quality threshold so we get more pages processed
	if engine.ExtractorFactory != nil {
		engine.ExtractorFactory.Config.QualityThreshold = 0.2
		engine.ExtractorFactory.Config.ExtractLinks = true
		engine.ExtractorFactory.Config.MinTextLength = 50
	}

	var linkSet = make(map[string]bool)
	var linkMutex sync.RWMutex
	var processedPages int32

	outputFile := "collected_links.txt"
	file, err := os.Create(outputFile)
	if err != nil {
		logger.Fatal("Failed to create output file", zap.Error(err))
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	defer writer.Flush()

	logger.Info("Starting link collection crawler",
		zap.String("output_file", outputFile),
		zap.Int("target_links", 5000),
		zap.Int("max_pages", crawlerConfig.MaxPages))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := engine.Start(ctx); err != nil {
		logger.Fatal("Failed to start crawler", zap.Error(err))
	}

	go func() {
		results := engine.GetResults()
		for result := range results {
			if result == nil {
				continue
			}

			processed := atomic.AddInt32(&processedPages, 1)

			if result.Success && result.ExtractedContent != nil {
				content := result.ExtractedContent

				logger.Info("Successfully processed page",
					zap.String("url", result.URL),
					zap.Int("links_found", len(content.Links)),
					zap.Int32("pages_processed", processed),
					zap.String("title", content.Title))

				// Collect links from this page
				linkMutex.Lock()
				for _, link := range content.Links {
					if isValidLink(link.URL) && !linkSet[link.URL] {
						linkSet[link.URL] = true

						// Write link to file immediately and flush
						fmt.Fprintf(writer, "%s\t%s\t%s\n",
							link.URL,
							strings.ReplaceAll(link.Text, "\n", " "),
							result.URL) // Source URL

						// Flush immediately to ensure link appears in file right away
						writer.Flush()
					}
				}
				currentLinkCount := len(linkSet)
				linkMutex.Unlock()

				logger.Info("Links collected so far",
					zap.Int("unique_links", currentLinkCount),
					zap.String("from_page", result.URL))

				// Only crawl external URLs (different domains) to avoid getting stuck on one site
				sourceHost, _ := frontier.ExtractHostFromURL(result.URL)
				externalLinksAdded := 0
				for _, link := range content.Links {
					if isValidCrawlTarget(link.URL, crawlerConfig.ExcludePatterns) {
						linkHost, _ := frontier.ExtractHostFromURL(link.URL)
						// Only add links from different domains to encourage diversity
						if linkHost != sourceHost && linkHost != "" {
							job := &crawler.CrawlJob{
								URL:       link.URL,
								Priority:  2, // Lower priority for discovered links
								Depth:     result.Job.Depth + 1,
								RequestID: fmt.Sprintf("external-%d", time.Now().UnixNano()),
								Context:   ctx,
							}
							engine.SubmitJob(job)
							externalLinksAdded++

							// Limit external links per page to avoid queue flooding
							if externalLinksAdded >= 5 {
								break
							}
						}
					}
				}

				// Check if we've reached our target
				if currentLinkCount >= 5000 {
					logger.Info("Target link count reached!", zap.Int("total_links", currentLinkCount))
					cancel() // Stop crawling
					return
				}

			} else if result.Error != nil {
				logger.Warn("Page crawl failed",
					zap.String("url", result.URL),
					zap.Error(result.Error),
					zap.Bool("retryable", result.Retryable))
			}
		}
	}()

	for i, seedURL := range crawlerConfig.SeedURLs {
		job := &crawler.CrawlJob{
			URL:       seedURL,
			Priority:  1,
			Depth:     0,
			RequestID: fmt.Sprintf("seed-%d", i),
			Context:   ctx,
		}

		logger.Info("Submitting seed URL", zap.String("url", seedURL))
		if err := engine.SubmitJob(job); err != nil {
			logger.Error("Failed to submit seed job", zap.String("url", seedURL), zap.Error(err))
		}

		time.Sleep(200 * time.Millisecond)
	}

	<-ctx.Done()

	logger.Info("Shutting down crawler...")
	if err := engine.Stop(); err != nil {
		logger.Error("Error stopping crawler", zap.Error(err))
	}

	linkMutex.RLock()
	finalLinkCount := len(linkSet)
	linkMutex.RUnlock()

	finalProcessedPages := atomic.LoadInt32(&processedPages)

	fmt.Fprintf(writer, "\n# CRAWL SUMMARY\n")
	fmt.Fprintf(writer, "# Total unique links collected: %d\n", finalLinkCount)
	fmt.Fprintf(writer, "# Pages processed: %d\n", finalProcessedPages)
	fmt.Fprintf(writer, "# Crawl completed at: %s\n", time.Now().Format(time.RFC3339))

	metrics := engine.GetMetrics()
	if metrics != nil {
		fmt.Fprintf(writer, "# Crawler metrics:\n")
		fmt.Fprintf(writer, "#   Total jobs: %d\n", metrics.TotalJobs)
		fmt.Fprintf(writer, "#   Successful jobs: %d\n", metrics.SuccessfulJobs)
		fmt.Fprintf(writer, "#   Failed jobs: %d\n", metrics.FailedJobs)
		fmt.Fprintf(writer, "#   Jobs per second: %.2f\n", metrics.JobsPerSecond)
		fmt.Fprintf(writer, "#   Uptime: %s\n", metrics.Uptime.String())
	}

	writer.Flush()

	logger.Info("Link collection completed!",
		zap.String("output_file", outputFile),
		zap.Int("unique_links_collected", finalLinkCount),
		zap.Int32("pages_processed", finalProcessedPages))

	if finalLinkCount < 5000 {
		logger.Info("Note: Collected fewer than target 5000 links. Consider adding more seed URLs or increasing MaxPages.")
	}
}

func isValidLink(url string) bool {
	if url == "" {
		return false
	}

	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return false
	}

	if len(url) > 500 {
		return false
	}

	if strings.Count(url, "&") > 10 {
		return false
	}

	excludePatterns := []string{
		"/logout", "/login", "/signin", "/signup", "/register",
		"/admin", "/wp-admin", "/wp-login",
		".css", ".js", ".json", ".xml", ".txt",
		".jpg", ".jpeg", ".png", ".gif", ".svg", ".ico",
		".pdf", ".doc", ".docx", ".zip", ".exe",
		"/api/", "/ajax/", "/webhook/",
		"javascript:", "mailto:", "tel:",
	}

	urlLower := strings.ToLower(url)
	for _, pattern := range excludePatterns {
		if strings.Contains(urlLower, pattern) {
			return false
		}
	}

	return true
}

func isValidCrawlTarget(url string, excludePatterns []string) bool {
	if !isValidLink(url) {
		return false
	}

	urlLower := strings.ToLower(url)
	for _, pattern := range excludePatterns {
		if strings.Contains(urlLower, strings.ToLower(pattern)) {
			return false
		}
	}

	return true
}
