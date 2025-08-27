// Package quert provides a public API for the Quert web crawler.
package quert

import (
	"github.com/Almahr1/quert/internal/config"
	"github.com/Almahr1/quert/internal/crawler"
	"github.com/Almahr1/quert/internal/extractor"
)

// Re-export all configuration types
type (
	Config           = config.Config
	CrawlerConfig    = config.CrawlerConfig
	RateLimitConfig  = config.RateLimitConfig
	ContentConfig    = config.ContentConfig
	DedupConfig      = config.DedupConfig
	StorageConfig    = config.StorageConfig
	MonitoringConfig = config.MonitoringConfig
	HTTPConfig       = config.HTTPConfig
	FrontierConfig   = config.FrontierConfig
	RobotsConfig     = config.RobotsConfig
	RedisConfig      = config.RedisConfig
	SecurityConfig   = config.SecurityConfig
	FeatureConfig    = config.FeatureConfig
)

// Re-export crawler types
type (
	CrawlerEngine  = crawler.CrawlerEngine
	CrawlJob       = crawler.CrawlJob
	CrawlResult    = crawler.CrawlResult
	CrawlerMetrics = crawler.CrawlerMetrics
)

// Re-export extractor types
type (
	ExtractedContent = extractor.ExtractedContent
	ExtractedLink    = extractor.ExtractedLink
	ExtractedImage   = extractor.ExtractedImage
	ContentMetadata  = extractor.ContentMetadata
	ExtractorFactory = extractor.ExtractorFactory
	ExtractorConfig  = extractor.ExtractorConfig
)

// Re-export constructor functions
var (
	NewCrawlerEngine        = crawler.NewCrawlerEngine
	NewExtractorFactory     = extractor.NewExtractorFactory
	GetDefaultExtractorConfig = extractor.GetDefaultExtractorConfig
)

// Re-export configuration functions
var (
	LoadConfig = config.LoadConfig
)