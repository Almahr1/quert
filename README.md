# Quert

A (hopefully) performant concurrent web crawler built in Go, designed specifically for collecting text data for LLM training pipelines. Aimed at being ethical and performant, adhering to robots.txt rules, and avoiding super-crazy crawling.

## Requirements

- Go 1.23.0 or later
- See `go.mod` for complete dependency list with versions

## Installation

```bash
git clone https://github.com/Almahr1/quert.git
cd quert
go mod download
```

## Usage

### Basic Example (Private API)

```go
package main

import (
    "context"
    "fmt"
    "log"
    "time"

    "github.com/Almahr1/quert/internal/config"
    "github.com/Almahr1/quert/internal/crawler"
    "go.uber.org/zap"
)

func main() {
    // Create logger
    logger, err := zap.NewDevelopment()
    if err != nil {
        log.Fatalf("Failed to create logger: %v", err)
    }
    defer logger.Sync()

    // Configure crawler
    crawlerConfig := &config.CrawlerConfig{
        MaxPages:          100,
        MaxDepth:          2,
        ConcurrentWorkers: 5,
        RequestTimeout:    15 * time.Second,
        UserAgent:         "Quert/1.0 (+https://github.com/Almahr1/quert)",
        SeedURLs:          []string{"https://example.com"},
    }

    httpConfig := &config.HTTPConfig{
        MaxIdleConnections:        50,
        MaxIdleConnectionsPerHost: 5,
        IdleConnectionTimeout:     30 * time.Second,
        Timeout:                   15 * time.Second,
        DialTimeout:               5 * time.Second,
    }

    // Create and start crawler engine
    engine := crawler.NewCrawlerEngine(crawlerConfig, httpConfig, nil, logger)
    ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
    defer cancel()

    if err := engine.Start(ctx); err != nil {
        log.Fatal(err)
    }
    defer engine.Stop()

    // Process results in background
    go func() {
        for result := range engine.GetResults() {
            if result.Success {
                fmt.Printf("✅ Crawled: %s (Status: %d, Length: %d)\n",
                    result.URL, result.StatusCode, len(result.Body))
                if result.ExtractedContent != nil {
                    fmt.Printf("   Title: %s\n", result.ExtractedContent.Title)
                    fmt.Printf("   Words: %d, Links: %d\n",
                        result.ExtractedContent.Metadata.WordCount, len(result.ExtractedContent.Links))
                }
            } else {
                fmt.Printf("❌ Failed: %s - %v\n", result.URL, result.Error)
            }
        }
    }()

    // Submit crawl jobs
    for _, url := range crawlerConfig.SeedURLs {
        job := &crawler.CrawlJob{
            URL:       url,
            Priority:  1,
            Depth:     0,
            Context:   ctx,
            RequestID: fmt.Sprintf("job-%d", time.Now().UnixNano()),
        }

        if err := engine.SubmitJob(job); err != nil {
            logger.Error("Failed to submit job", zap.String("url", url), zap.Error(err))
        }
    }

    // Wait and show final metrics
    time.Sleep(30 * time.Second)
    metrics := engine.GetMetrics()
    fmt.Printf("\n📊 Final Stats: %d successful, %d failed, %.2f pages/sec\n",
        metrics.SuccessfulJobs, metrics.FailedJobs, metrics.JobsPerSecond)
}
```

### Configuration-based Example (Public API)

For use, load configuration from YAML/JSON files:

```go
package main

import (
    "context"
    "log"

    "github.com/Almahr1/quert/pkg/quert"
    "go.uber.org/zap"
)

func main() {
    // Load configuration from file
    config, err := quert.LoadConfig("config.yaml", nil)
    if err != nil {
        log.Fatal(err)
    }

    logger, _ := config.GetLogger()

    // Create crawler with loaded configuration
    engine := quert.NewCrawlerEngine(
        &config.Crawler,
        &config.HTTP,
        &config.Robots,
        logger,
    )

    ctx := context.Background()
    engine.Start(ctx)
    defer engine.Stop()

    // Your crawling logic here...
}
```

### Running Examples

Three example applications are provided:

```bash
# Basic crawling demonstration
make run-basic-crawl

# Simple example with minimal setup
make run-simple-example

# Comprehensive crawler with advanced features
make run-comprehensive-crawler
```

## Configuration

The system accepts configuration through YAML files, environment variables, or programmatic setup. Key configuration sections include:

- `crawler`: Worker count, depth limits, URL patterns, user agent
- `http`: Connection pooling, timeouts, compression settings
- `rate_limit`: Request rates, burst limits, per-host constraints
- `content`: Quality thresholds, text length limits, extraction settings

Example configuration file structure is provided in `config.yaml`.

## Content Extraction (Planned)

The extraction system is planned to support multiple content types through a factory pattern:

- **HTML**: Main content extraction, link parsing, metadata extraction
- **Plain Text**: Basic text processing with quality scoring
- **XML**: XML/RSS/Atom feed processing

Content quality is assessed using configurable metrics including text length, word count, sentence structure, and language detection.

## Rate Limiting

The crawler implements both global and per-host rate limiting:

- Global rate limiting controls overall request volume
- Per-host rate limiting ensures compliance with individual site constraints
- Adaptive rate adjustment responds to server response characteristics
- Robots.txt crawl-delay directives are automatically respected

## Testing

```bash
# Run all tests
make test

# Run component-specific tests
make test-config
make test-http
make test-url
make test-extractor
make test-crawler
make test-robots

# Run tests with race detection
make test-race
```

## Development

```bash
# Format code
make fmt

# Run linter
make lint

# Build binary
make build

# Quick development workflow
make quick
```

## Implementation Status

The project is currently in developement, I'd estimate around 50% done for now, still working on the actual content extraction which is a major part of the project.

## License

GNU General Public License v3.0

## Dependencies

Core dependencies:
- `github.com/PuerkitoBio/goquery v1.10.3` - HTML parsing and DOM manipulation
- `github.com/spf13/viper v1.20.1` - Configuration management
- `github.com/spf13/pflag v1.0.6` - Command line flags
- `github.com/temoto/robotstxt v1.1.2` - Robots.txt parsing
- `go.uber.org/zap v1.27.0` - Structured logging
- `golang.org/x/time v0.12.0` - Rate limiting primitives

Development dependencies:
- `github.com/stretchr/testify v1.10.0` - Testing framework

## Development Transparency

This project was built by humans, augmented by AI. I believe in using the best tools for the job:

*   **Human-Led:** The core architecture, design decisions, complex logic, and final implementation were meticulously planned and executed by a human developer.
*   **AI-Assisted:** To boost productivity, I utilized AI (primarily LLMs) for generating repetitive boilerplate code, comprehensive test cases, and initial drafts of documentation. All AI-generated content was reviewed, tested, and adapted to fit the project's standards. (some room for error, which is reviewwed occasionally)

This approach allowed me to focus my creative energy on solving hard problems rather than repetitive tasks.


## School Notice:

Currently in school which takes a lot of time away from coding. I will still occasionally work on it from time to time, but issues in the codebase or documentation could take a while before getting fixed.
