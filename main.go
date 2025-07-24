package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// fetchURL fetches the content of a given URL.
// It returns the response body as a byte slice and an error if any.
func fetchURL(url string) ([]byte, error) {
	fmt.Printf("Fetching URL: %s\n", url)
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	resp, err := client.Get(url)
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

func extractLinks(htmlContent []byte, baseURL string) ([]string, error) {
	// Create a new HTML tokenizer from the HTML content.
	doc, err := html.Parse(strings.NewReader(string(htmlContent)))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	var links []string
	var f func(*html.Node)
	f = func(n *html.Node) {
		// Check if the node is an element node and if its tag is "a" (anchor).
		if n.Type == html.ElementNode && n.Data == "a" {
			// Iterate over the attributes of the "a" tag.
			for _, a := range n.Attr {
				// If the attribute key is "href", we found a link.
				if a.Key == "href" {
					link := a.Val
					// Basic relative path handling (more robust solutions exist)
					if strings.HasPrefix(link, "/") && len(baseURL) > 0 {
						// Prepend base URL to relative paths.
						// This is a simplified approach. For production, consider url.Parse.
						link = strings.TrimSuffix(baseURL, "/") + link
					}
					if strings.HasPrefix(link, "#") { // Skip anchor links on the same page
						continue
					}
					links = append(links, link)
				}
			}
		}
		// Recursively call the function for child nodes.
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	// Start the traversal from the root of the HTML document.
	f(doc)
	return links, nil
}

func main() {
	targetURL := "https://gurawa.com" // A simple, safe URL to test with.

	body, err := fetchURL(targetURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching URL: %v\n", err)
		return
	}

	// For demonstration, print the first 500 characters of the body.
	// In a real crawler, you'd parse this HTML.
	fmt.Println("Successfully fetched URL content (first 500 chars):")
	if len(body) > 500 {
		fmt.Println(string(body[:500]))
	} else {
		fmt.Println(string(body))
	}
}
