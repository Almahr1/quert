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

** Under Review ** 

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
