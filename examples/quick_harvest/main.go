package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Almahr1/quert/internal/config"
	"github.com/Almahr1/quert/internal/crawler"
	"go.uber.org/zap"
)

func main() {
	logger, _ := zap.NewDevelopment()
	defer logger.Sync()

	// Quick configuration for fast link harvesting
	crawlerConfig := &config.CrawlerConfig{
		MaxPages:          100, // Process up to 100 pages
		MaxDepth:          2,   // Stay shallow for speed
		ConcurrentWorkers: 25,  // 4 workers for faster processing
		RequestTimeout:    10 * time.Second,
		UserAgent:         "Quert-LinkHarvester/1.0 Educational",
		SeedURLs: []string{
			// Sites known to have many links and be crawl-friendly
			"https://quotes.toscrape.com",
			"https://httpbin.org/links/20", // Generates 20 test links
			"https://news.ycombinator.com", // Lots of external links
			"https://example.com",
			// Wikipedia links, why not?
			"https://en.wikipedia.org/wiki/List_of_programming_languages",
			"https://en.wikipedia.org/wiki/Computer_science",
		},
		IncludePatterns: []string{".*"},
		ExcludePatterns: []string{
			".*\\.(css|js|jpg|png|gif|pdf|zip)$",
			"/logout", "/login", "/admin",
		},
	}

	httpConfig := &config.HTTPConfig{
		MaxIdleConnections:        20,
		MaxIdleConnectionsPerHost: 4,
		IdleConnectionTimeout:     30 * time.Second,
		DisableKeepAlives:         false,
		Timeout:                   15 * time.Second,
		DialTimeout:               5 * time.Second,
		TlsHandshakeTimeout:       10 * time.Second,
		ResponseHeaderTimeout:     10 * time.Second,
		DisableCompression:        false,
	}

	// Create output file
	outputFile := fmt.Sprintf("harvested_links_%s.txt", time.Now().Format("20060102_150405"))
	file, err := os.Create(outputFile)
	if err != nil {
		log.Fatal("Cannot create output file:", err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	defer writer.Flush()

	// Write header
	fmt.Fprintln(writer, "# Link Harvest Results")
	fmt.Fprintln(writer, "# Format: URL | Link Text | Source Page | Internal")
	fmt.Fprintln(writer, "# Generated:", time.Now().Format(time.RFC3339))
	fmt.Fprintln(writer, "")

	// Initialize crawler
	engine := crawler.NewCrawlerEngine(crawlerConfig, httpConfig, nil, logger)

	// Configure for maximum link extraction
	if engine.ExtractorFactory != nil {
		engine.ExtractorFactory.Config.QualityThreshold = 0.1 // Very low threshold
		engine.ExtractorFactory.Config.ExtractLinks = true
		engine.ExtractorFactory.Config.MinTextLength = 10
	}

	// Tracking variables
	var linksCollected int32
	var pagesProcessed int32
	var targetLinks int32 = 1000

	logger.Info("Starting quick link harvest",
		zap.String("output", outputFile),
		zap.Int32("target", targetLinks))

	// Start crawler
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	engine.Start(ctx)

	// Process results
	go func() {
		defer cancel() // Ensure we stop when done

		for result := range engine.GetResults() {
			if result == nil {
				continue
			}

			pages := atomic.AddInt32(&pagesProcessed, 1)

			if result.Success && result.ExtractedContent != nil {
				content := result.ExtractedContent

				logger.Info("Harvesting from page",
					zap.String("url", result.URL),
					zap.String("title", content.Title),
					zap.Int("links", len(content.Links)),
					zap.Int32("pages", pages))

				// Write all links from this page
				for _, link := range content.Links {
					if isUsefulLink(link.URL) {
						collected := atomic.AddInt32(&linksCollected, 1)

						// Write to file
						fmt.Fprintf(writer, "%s | %s | %s | %t\n",
							link.URL,
							cleanText(link.Text),
							result.URL,
							link.Internal)

						// Log progress every 50 links
						if collected%50 == 0 {
							logger.Info("Progress update",
								zap.Int32("collected", collected),
								zap.Int32("target", targetLinks))
						}

						// Check if we've reached target
						if collected >= targetLinks {
							logger.Info("Target reached!",
								zap.Int32("links_collected", collected),
								zap.Int32("pages_processed", pages))
							return
						}

						// Submit internal links for further crawling
						if link.Internal && result.Job.Depth < 2 {
							job := &crawler.CrawlJob{
								URL:       link.URL,
								Priority:  2,
								Depth:     result.Job.Depth + 1,
								RequestID: fmt.Sprintf("harvest-%d", time.Now().UnixNano()),
								Context:   ctx,
							}
							engine.SubmitJob(job)
						}
					}
				}

			} else if result.Error != nil {
				logger.Warn("Page failed",
					zap.String("url", result.URL),
					zap.Error(result.Error))
			}
		}
	}()

	// Submit seed URLs
	for i, seedURL := range crawlerConfig.SeedURLs {
		job := &crawler.CrawlJob{
			URL:       seedURL,
			Priority:  1,
			Depth:     0,
			RequestID: fmt.Sprintf("seed-%d", i),
			Context:   ctx,
		}

		logger.Info("Submitting seed", zap.String("url", seedURL))
		engine.SubmitJob(job)
		time.Sleep(500 * time.Millisecond)
	}

	// Wait for completion
	<-ctx.Done()

	// Shutdown
	logger.Info("Shutting down...")
	engine.Stop()

	// Final stats
	finalLinks := atomic.LoadInt32(&linksCollected)
	finalPages := atomic.LoadInt32(&pagesProcessed)

	// Write summary to file
	fmt.Fprintln(writer, "")
	fmt.Fprintln(writer, "# HARVEST SUMMARY")
	fmt.Fprintf(writer, "# Links collected: %d\n", finalLinks)
	fmt.Fprintf(writer, "# Pages processed: %d\n", finalPages)
	fmt.Fprintf(writer, "# Completion time: %s\n", time.Now().Format(time.RFC3339))

	if metrics := engine.GetMetrics(); metrics != nil {
		fmt.Fprintf(writer, "# Crawler stats: %d total, %d success, %d failed\n",
			metrics.TotalJobs, metrics.SuccessfulJobs, metrics.FailedJobs)
		fmt.Fprintf(writer, "# Performance: %.2f jobs/sec, %.2f%% error rate\n",
			metrics.JobsPerSecond, metrics.ErrorRate)
	}

	logger.Info("Link harvest completed!",
		zap.String("file", outputFile),
		zap.Int32("links", finalLinks),
		zap.Int32("pages", finalPages))

	fmt.Printf("\n✅ Success! Collected %d links from %d pages\n", finalLinks, finalPages)
	fmt.Printf("📄 Results saved to: %s\n", outputFile)
}

// isUsefulLink filters out unwanted URLs
func isUsefulLink(url string) bool {
	if len(url) < 10 || len(url) > 300 {
		return false
	}

	// Must be HTTP/HTTPS
	if !strings.HasPrefix(url, "http") {
		return false
	}

	// Skip common non-content URLs
	unwanted := []string{
		"javascript:", "mailto:", "tel:",
		".css", ".js", ".jpg", ".png", ".gif", ".pdf",
		"/logout", "/login", "/admin", "/wp-admin",
		"/search?", "/tag/", "/category/",
	}

	urlLower := strings.ToLower(url)
	for _, bad := range unwanted {
		if strings.Contains(urlLower, bad) {
			return false
		}
	}

	return true
}

// cleanText removes newlines and limits length for file output
func cleanText(text string) string {
	// Remove newlines and extra spaces
	cleaned := strings.ReplaceAll(text, "\n", " ")
	cleaned = strings.ReplaceAll(cleaned, "\r", " ")
	cleaned = strings.TrimSpace(cleaned)

	// Limit length
	if len(cleaned) > 100 {
		cleaned = cleaned[:100] + "..."
	}

	return cleaned
}
