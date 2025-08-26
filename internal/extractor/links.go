package extractor

import (
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/Almahr1/quert/internal/frontier"
	"go.uber.org/zap"
)

// ExtractLinks extracts links from HTML content using goquery and existing URL processing
func (h *HTMLContentExtractor) ExtractLinks(content []byte, baseURL string) ([]ExtractedLink, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(content)))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML for link extraction: %w", err)
	}

	return h.ExtractLinksFromDocument(doc, baseURL)
}

// ExtractLinksFromDocument extracts links from a goquery document using existing frontier utilities
func (h *HTMLContentExtractor) ExtractLinksFromDocument(doc *goquery.Document, baseURL string) ([]ExtractedLink, error) {
	var links []ExtractedLink
	seenLinks := make(map[string]bool) // For deduplication

	// Extract base domain for internal link detection
	baseDomain, err := frontier.ExtractDomain(baseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to extract base domain: %w", err)
	}

	doc.Find("a[href]").Each(func(i int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if !exists || href == "" {
			return
		}

		// Skip javascript, mailto, tel, and other non-HTTP links
		if strings.HasPrefix(href, "javascript:") ||
			strings.HasPrefix(href, "mailto:") ||
			strings.HasPrefix(href, "tel:") ||
			strings.HasPrefix(href, "#") {
			return
		}

		// Resolve relative URLs using existing logic
		linkURL := h.ResolveURLUsingFrontier(href, baseURL)
		if linkURL == "" {
			return
		}

		// Skip if we've already seen this link
		if seenLinks[linkURL] {
			return
		}
		seenLinks[linkURL] = true

		// Extract link text and attributes
		linkText := strings.TrimSpace(s.Text())
		title, _ := s.Attr("title")
		rel, _ := s.Attr("rel")

		// Determine if link is internal using existing domain extraction
		isInternal := false
		linkDomain, err := frontier.ExtractDomain(linkURL)
		if err == nil {
			isInternal = linkDomain == baseDomain
		}

		link := ExtractedLink{
			URL:      linkURL,
			Text:     linkText,
			Title:    title,
			Rel:      rel,
			Internal: isInternal,
		}

		links = append(links, link)
	})

	h.Logger.Debug("extracted links from document",
		zap.Int("total_links", len(links)),
		zap.String("base_url", baseURL))

	return links, nil
}

// ExtractImages extracts images from HTML content using goquery and existing URL processing
func (h *HTMLContentExtractor) ExtractImages(content []byte, baseURL string) ([]ExtractedImage, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(content)))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML for image extraction: %w", err)
	}

	return h.ExtractImagesFromDocument(doc, baseURL)
}

// ExtractImagesFromDocument extracts images from a goquery document using existing URL processing
func (h *HTMLContentExtractor) ExtractImagesFromDocument(doc *goquery.Document, baseURL string) ([]ExtractedImage, error) {
	var images []ExtractedImage
	seenImages := make(map[string]bool) // For deduplication

	// Extract img tags
	doc.Find("img").Each(func(i int, s *goquery.Selection) {
		src, exists := s.Attr("src")
		if !exists || src == "" {
			// Try data-src for lazy-loaded images
			if dataSrc, dataExists := s.Attr("data-src"); dataExists && dataSrc != "" {
				src = dataSrc
			} else {
				return
			}
		}

		// Resolve relative URLs using existing logic
		imageURL := h.ResolveURLUsingFrontier(src, baseURL)
		if imageURL == "" {
			return
		}

		// Skip if we've already seen this image
		if seenImages[imageURL] {
			return
		}
		seenImages[imageURL] = true

		// Extract image attributes
		alt, _ := s.Attr("alt")
		title, _ := s.Attr("title")
		width, _ := s.Attr("width")
		height, _ := s.Attr("height")

		// Try to find caption from surrounding elements
		caption := h.FindImageCaption(s)

		image := ExtractedImage{
			URL:     imageURL,
			Alt:     alt,
			Title:   title,
			Width:   width,
			Height:  height,
			Caption: caption,
		}

		images = append(images, image)
	})

	// Extract CSS background images
	doc.Find("*[style*='background-image']").Each(func(i int, s *goquery.Selection) {
		style, exists := s.Attr("style")
		if !exists {
			return
		}

		imageURL := h.ExtractBackgroundImageURL(style, baseURL)
		if imageURL != "" && !seenImages[imageURL] {
			seenImages[imageURL] = true

			image := ExtractedImage{
				URL:     imageURL,
				Alt:     "",
				Title:   "",
				Width:   "",
				Height:  "",
				Caption: "",
			}

			images = append(images, image)
		}
	})

	h.Logger.Debug("extracted images from document",
		zap.Int("total_images", len(images)),
		zap.String("base_url", baseURL))

	return images, nil
}

// ResolveURLUsingFrontier resolves URLs using the existing frontier URL processing logic
func (h *HTMLContentExtractor) ResolveURLUsingFrontier(href, baseURL string) string {
	if href == "" {
		return ""
	}

	// If URL is already absolute, validate and return
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		// Parse and validate using frontier utilities
		if _, err := frontier.ParseURL(href); err != nil {
			return ""
		}
		return href
	}

	// Handle relative URLs
	var resolvedURL string

	if strings.HasPrefix(href, "//") {
		// Protocol-relative URL
		if strings.HasPrefix(baseURL, "https://") {
			resolvedURL = "https:" + href
		} else {
			resolvedURL = "http:" + href
		}
	} else if strings.HasPrefix(href, "/") {
		// Root-relative URL
		baseHost, err := frontier.ExtractHostFromURL(baseURL)
		if err != nil {
			return ""
		}
		
		if strings.HasPrefix(baseURL, "https://") {
			resolvedURL = "https://" + baseHost + href
		} else {
			resolvedURL = "http://" + baseHost + href
		}
	} else {
		// Path-relative URL - more complex resolution needed
		// For now, treat as root-relative if it doesn't start with http
		if !strings.Contains(href, "://") {
			baseHost, err := frontier.ExtractHostFromURL(baseURL)
			if err != nil {
				return ""
			}
			
			if strings.HasPrefix(baseURL, "https://") {
				resolvedURL = "https://" + baseHost + "/" + href
			} else {
				resolvedURL = "http://" + baseHost + "/" + href
			}
		} else {
			return href
		}
	}

	// Validate the resolved URL using frontier utilities
	if _, err := frontier.ParseURL(resolvedURL); err != nil {
		return ""
	}

	return resolvedURL
}

// FindImageCaption attempts to find a caption for an image
func (h *HTMLContentExtractor) FindImageCaption(imgSelection *goquery.Selection) string {
	// Check parent figure element
	if figure := imgSelection.Parent().Filter("figure"); figure.Length() > 0 {
		if figcaption := figure.Find("figcaption"); figcaption.Length() > 0 {
			return strings.TrimSpace(figcaption.Text())
		}
	}

	// Check parent div with caption class
	if parent := imgSelection.Parent().Filter("div"); parent.Length() > 0 {
		if caption := parent.Find(".caption, .img-caption, .image-caption"); caption.Length() > 0 {
			return strings.TrimSpace(caption.Text())
		}
	}

	// Check next sibling for caption
	if nextSibling := imgSelection.Next(); nextSibling.Length() > 0 {
		if nextSibling.Is(".caption, .img-caption, .image-caption, figcaption") {
			return strings.TrimSpace(nextSibling.Text())
		}
	}

	return ""
}

// ExtractBackgroundImageURL extracts background image URL from CSS style
func (h *HTMLContentExtractor) ExtractBackgroundImageURL(style, baseURL string) string {
	// Simple extraction of URL from background-image CSS
	urlStart := strings.Index(style, "url(")
	if urlStart == -1 {
		return ""
	}

	urlStart += 4 // Skip "url("
	urlEnd := strings.Index(style[urlStart:], ")")
	if urlEnd == -1 {
		return ""
	}

	imageURL := strings.Trim(style[urlStart:urlStart+urlEnd], `"' `)
	
	// Resolve using existing frontier logic
	return h.ResolveURLUsingFrontier(imageURL, baseURL)
}

// FilterLinksByType filters links based on various criteria
func (h *HTMLContentExtractor) FilterLinksByType(links []ExtractedLink, linkType string) []ExtractedLink {
	var filtered []ExtractedLink

	for _, link := range links {
		switch linkType {
		case "internal":
			if link.Internal {
				filtered = append(filtered, link)
			}
		case "external":
			if !link.Internal {
				filtered = append(filtered, link)
			}
		case "nofollow":
			if strings.Contains(link.Rel, "nofollow") {
				filtered = append(filtered, link)
			}
		case "follow":
			if !strings.Contains(link.Rel, "nofollow") {
				filtered = append(filtered, link)
			}
		default:
			filtered = append(filtered, link)
		}
	}

	return filtered
}

// FilterImagesByType filters images based on various criteria
func (h *HTMLContentExtractor) FilterImagesByType(images []ExtractedImage, imageType string) []ExtractedImage {
	var filtered []ExtractedImage

	for _, image := range images {
		switch imageType {
		case "with_alt":
			if image.Alt != "" {
				filtered = append(filtered, image)
			}
		case "without_alt":
			if image.Alt == "" {
				filtered = append(filtered, image)
			}
		case "with_caption":
			if image.Caption != "" {
				filtered = append(filtered, image)
			}
		case "large":
			if h.IsLargeImage(image) {
				filtered = append(filtered, image)
			}
		default:
			filtered = append(filtered, image)
		}
	}

	return filtered
}

// IsLargeImage determines if an image is considered large based on dimensions
func (h *HTMLContentExtractor) IsLargeImage(image ExtractedImage) bool {
	// Simple heuristic based on dimension strings
	if image.Width != "" {
		if strings.HasSuffix(image.Width, "px") {
			widthStr := strings.TrimSuffix(image.Width, "px")
			if len(widthStr) >= 3 { // 3+ digit width (100px+)
				return true
			}
		}
	}

	if image.Height != "" {
		if strings.HasSuffix(image.Height, "px") {
			heightStr := strings.TrimSuffix(image.Height, "px")
			if len(heightStr) >= 3 { // 3+ digit height (100px+)
				return true
			}
		}
	}

	// Default to considering it large if no size info available
	return true
}