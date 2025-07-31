package main

import (
	"bufio"
	"flag"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/time/rate" // For rate limiting
)

// Task represents a URL to be crawled at a specific depth.
type Task struct {
	URL   string
	Depth int
}

// CrawlResult represents the outcome of crawling a URL.
type CrawlResult struct {
	URL      string
	Links    []string
	Error    error
	Depth    int
	RootDomain string // Store the root domain for filtering
}

// Crawler struct encapsulates the crawler's state and configuration.
type Crawler struct {
	visitedMu   sync.Mutex
	visited     map[string]bool
	maxDepth    int
	numWorkers  int
	rateLimiter *rate.Limiter // Rate limiter for ethical crawling
	outputFile  string        // File to write discovered URLs
	rootDomain  string        // The root domain to stay within
}

// NewCrawler creates and initializes a new Crawler instance.
func NewCrawler(startURL string, maxDepth, numWorkers int, qps float64, outputFile string) (*Crawler, error) {
	parsedURL, err := url.Parse(startURL)
	if err != nil {
		return nil, fmt.Errorf("invalid start URL: %w", err)
	}
	rootDomain := parsedURL.Hostname()
	if strings.Contains(rootDomain, "www.") { // Normalize www. to bare domain
		rootDomain = strings.TrimPrefix(rootDomain, "www.")
	}

	return &Crawler{
		visited:     make(map[string]bool),
		maxDepth:    maxDepth,
		numWorkers:  numWorkers,
		rateLimiter: rate.NewLimiter(rate.Limit(qps), 1), // QPS, burst of 1
		outputFile:  outputFile,
		rootDomain:  rootDomain,
	}, nil
}

// fetchURL fetches the content of a given URL.
func fetchURL(urlStr string) ([]byte, error) {
	log.Printf("Fetching: %s", urlStr)
	client := &http.Client{
		Timeout: 15 * time.Second, // Increased timeout for potentially slower sites
	}

	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "MyGoWebCrawler/1.0 (+https://github.com/your_repo_link_here)") // Ethical User-Agent

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make GET request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status code: %d %s", resp.StatusCode, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	return body, nil
}

// extractLinks parses HTML content and returns a slice of all found links.
// It also resolves relative URLs against the base URL and filters by root domain.
func extractLinks(htmlContent []byte, baseURLStr, rootDomain string) ([]string, error) {
	doc, err := html.Parse(strings.NewReader(string(htmlContent)))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	var links []string
	baseParsedURL, err := url.Parse(baseURLStr)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL for link extraction: %w", err)
	}

	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key == "href" {
					link := a.Val
					parsedLink, err := url.Parse(link)
					if err != nil {
						// log.Printf("Warning: Malformed URL found: %s (from %s)", link, baseURLStr)
						continue // Skip malformed URLs
					}
					resolvedLink := baseParsedURL.ResolveReference(parsedLink).String()

					// Filter out in-page anchors
					if strings.HasPrefix(resolvedLink, "#") {
						continue
					}

					// Validate and normalize the resolved URL
					finalParsedLink, err := url.Parse(resolvedLink)
					if err != nil {
						// log.Printf("Warning: Could not re-parse resolved link: %s", resolvedLink)
						continue
					}
					// Remove fragment (everything after #) for unique URL tracking
					finalParsedLink.Fragment = ""
					finalParsedLink.RawFragment = ""
					normalizedLink := finalParsedLink.String()

					// Filter by same domain
					linkDomain := finalParsedLink.Hostname()
					if strings.Contains(linkDomain, "www.") {
						linkDomain = strings.TrimPrefix(linkDomain, "www.")
					}

					if linkDomain == rootDomain { // Only add links from the same root domain
						links = append(links, normalizedLink)
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)
	return links, nil
}

// crawlWorker fetches and parses a URL, sending results back on a channel.
func (c *Crawler) crawlWorker(id int, tasks <-chan Task, results chan<- CrawlResult, workerWg *sync.WaitGroup) {
	defer workerWg.Done() // Signal that this worker is done when the goroutine exits

	for task := range tasks {
		// Respect the rate limit before making an HTTP request
		c.rateLimiter.Wait(context.Background()) // Block until allowed, possibly adjusted by depth for slower crawling deeper

		// Check visited status under mutex before processing
		c.visitedMu.Lock()
		if c.visited[task.URL] {
			c.visitedMu.Unlock()
			continue // Already visited, skip
		}
		c.visited[task.URL] = true // Mark as visited
		c.visitedMu.Unlock()

		log.Printf("[Worker %d] Processing (Depth %d): %s", id, task.Depth, task.URL)

		body, err := fetchURL(task.URL)
		if err != nil {
			results <- CrawlResult{URL: task.URL, Error: fmt.Errorf("fetch error: %w", err), Depth: task.Depth}
			continue
		}

		// Use the crawler's rootDomain for filtering during link extraction
		links, err := extractLinks(body, task.URL, c.rootDomain)
		if err != nil {
			results <- CrawlResult{URL: task.URL, Error: fmt.Errorf("parse error: %w", err), Depth: task.Depth}
			continue
		}

		results <- CrawlResult{URL: task.URL, Links: links, Error: nil, Depth: task.Depth, RootDomain: c.rootDomain}
	}
}

// Run starts the concurrent crawling process.
func (c *Crawler) Run(startURL string) {
	tasks := make(chan Task, c.numWorkers*2)     // Buffered channel for tasks
	results := make(chan CrawlResult, c.numWorkers*2) // Buffered channel for results

	var workerWg sync.WaitGroup // WaitGroup for all worker goroutines

	// Start worker goroutines
	for i := 0; i < c.numWorkers; i++ {
		workerWg.Add(1)
		go c.crawlWorker(i+1, tasks, results, &workerWg)
	}

	// Coordinator goroutine to manage the BFS queue and shutdown
	var coordinatorWg sync.WaitGroup
	coordinatorWg.Add(1)
	go func() {
		defer coordinatorWg.Done()
		
		// This map helps track unique URLs to be added to the next depth's queue
		nextDepthURLs := make(map[string]bool)
		
		// To track the number of URLs expected for each depth to know when to transition
		// Key: depth, Value: count of URLs at that depth either in tasks channel or results pending
		pendingURLsAtDepth := make(map[int]int)

		// File writer for output
		var file *os.File
		var writer *bufio.Writer
		if c.outputFile != "" {
			var err error
			file, err = os.OpenFile(c.outputFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				log.Fatalf("Failed to open output file %s: %v", c.outputFile, err)
			}
			defer file.Close()
			writer = bufio.NewWriter(file)
			defer writer.Flush()
		}

		// Send initial task
		tasks <- Task{URL: startURL, Depth: 0}
		pendingURLsAtDepth[0] = 1 // One URL at depth 0 initially

		activeProcessing := 1 // Tracks URLs sent to workers but not yet processed (in-flight)

		for {
			select {
			case result, ok := <-results:
				if !ok { // results channel closed, no more results coming
					// Check if all tasks have been processed and no more tasks are being generated
					if activeProcessing == 0 && len(tasks) == 0 && len(pendingURLsAtDepth) == 0 {
						log.Println("Coordinator: All results processed, shutting down.")
						return // All done
					}
					// If results channel is closed but there might still be tasks in flight or being generated,
					// wait for them or rely on timeout.
					continue
				}

				activeProcessing-- // One less URL being processed

				if result.Error != nil {
					log.Printf("Error processing %s (Depth %d): %v", result.URL, result.Depth, result.Error)
				} else {
					log.Printf("Processed (Depth %d): %s", result.Depth, result.URL)
					if writer != nil {
						if _, err := writer.WriteString(result.URL + "\n"); err != nil {
							log.Printf("Failed to write URL to file: %v", err)
						}
					}
				}

				// Decrement the count for the current depth
				pendingURLsAtDepth[result.Depth]--

				// If we are within max depth, add newly found links to the next depth's queue
				if result.Depth < c.maxDepth {
					for _, link := range result.Links {
						c.visitedMu.Lock()
						if !c.visited[link] { // Only consider new, unvisited links
							nextDepthURLs[link] = true // Add to set for next depth
						}
						c.visitedMu.Unlock()
					}
				}

				// Check for depth transition:
				// If all URLs at the current depth have been processed
				if pendingURLsAtDepth[result.Depth] == 0 {
					delete(pendingURLsAtDepth, result.Depth) // Clean up this depth from the map

					// If there are URLs for the next depth, push them to tasks channel
					// and update pendingURLsAtDepth for the new depth
					if len(nextDepthURLs) > 0 && result.Depth < c.maxDepth {
						nextDepth := result.Depth + 1
						log.Printf("--- Transitioning to Depth %d (Found %d new URLs) ---", nextDepth, len(nextDepthURLs))
						
						countForNextDepth := 0
						for link := range nextDepthURLs {
							c.visitedMu.Lock()
							if !c.visited[link] { // Double check visited just in case
								tasks <- Task{URL: link, Depth: nextDepth}
								activeProcessing++
								countForNextDepth++
							}
							c.visitedMu.Unlock()
						}
						pendingURLsAtDepth[nextDepth] = countForNextDepth
						nextDepthURLs = make(map[string]bool) // Clear for the next level
					} else if result.Depth >= c.maxDepth {
						log.Printf("Reached max depth (%d), not queuing further links.", result.Depth)
					}
				}
				
			case <-time.After(3 * time.Second): // Timeout if no activity for a while
				// This handles cases where all tasks might be in flight but not yet returned,
				// or if there's a very long pause in between tasks.
				// If no active processing and no more tasks queued for workers
				if activeProcessing == 0 && len(tasks) == 0 {
					// Also check if no depths are pending more URLs
					allDepthsProcessed := true
					for _, count := range pendingURLsAtDepth {
						if count > 0 {
							allDepthsProcessed = false
							break
						}
					}
					if allDepthsProcessed {
						log.Println("Coordinator: No active processing, no pending tasks, and all depths processed. Initiating shutdown.")
						return // All done
					}
				}
			}
		}
	}()

	// Wait for the coordinator to finish (which ensures all depths are managed and tasks are pushed)
	coordinatorWg.Wait()

	// Close the tasks channel AFTER the coordinator has finished pushing all tasks
	close(tasks)
	log.Println("Closed tasks channel. Waiting for workers to finish remaining work...")

	// Wait for all workers to finish processing any remaining tasks
	workerWg.Wait()
	log.Println("All workers finished.")

	// Close the results channel after all workers are guaranteed to be done
	close(results)
	log.Println("Closed results channel.")

	log.Printf("Concurrent Crawling finished! Total Visited URLs: %d", len(c.visited))
}

func main() {
	// Command-line arguments
	startURL := flag.String("url", "", "The starting URL for the crawler (required)")
	maxDepth := flag.Int("depth", 1, "Maximum crawl depth")
	numWorkers := flag.Int("workers", 5, "Number of concurrent worker goroutines")
	qps := flag.Float64("qps", 1.0, "Queries per second (rate limit) to target hosts")
	outputFile := flag.String("output", "crawled_urls.txt", "File to write discovered URLs")

	flag.Parse()

	if *startURL == "" {
		fmt.Println("Error: --url is required.")
		flag.Usage()
		os.Exit(1)
	}

	// Set up logging
	log.SetOutput(os.Stdout)
	log.SetFlags(log.LstdFlags | log.Lshortfile) // Include file name and line number

	crawler, err := NewCrawler(*startURL, *maxDepth, *numWorkers, *qps, *outputFile)
	if err != nil {
		log.Fatalf("Failed to initialize crawler: %v", err)
	}

	log.Printf("Starting crawler from %s with max depth %d, %d workers, and %f QPS.",
		*startURL, *maxDepth, *numWorkers, *qps)
	log.Printf("Crawling within domain: %s", crawler.rootDomain)
	if *outputFile != "" {
		log.Printf("Discovered URLs will be written to: %s", *outputFile)
	}

	crawler.Run(*startURL)
}
