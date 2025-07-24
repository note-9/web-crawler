package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// fetchURL fetches the content of a given URL.
// It returns the response body as a byte slice and an error if any.
func fetchURL(url string) ([]byte, error) {
	fmt.Printf("Fetching URL: %s\n", url)

	// Create an HTTP client with a timeout to prevent hanging indefinitely.
	client := &http.Client{
		Timeout: 10 * time.Second, // 10-second timeout for the entire request
	}

	// Make an HTTP GET request to the URL.
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to make GET request: %w", err)
	}
	defer resp.Body.Close() // Ensure the response body is closed to prevent resource leaks.

	// Check if the HTTP status code indicates success (200 OK).
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status code: %d %s", resp.StatusCode, resp.Status)
	}

	// Read the entire response body.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	return body, nil
}

func main() {
	targetURL := "https://example.com" // A simple, safe URL to test with.

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
