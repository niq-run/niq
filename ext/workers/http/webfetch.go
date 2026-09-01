package http

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

const maxWebfetchBytes = 1 * 1024 * 1024 // 1 MB

func (w *Worker) handleWebfetch(args map[string]any) (string, error) {
	url, _ := args["url"].(string)
	if url == "" {
		return "", fmt.Errorf("url is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("webfetch: build request: %w", err)
	}
	req.Header.Set("User-Agent", "niq/1.0 webfetch")

	resp, err := w.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("webfetch: %w", err)
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, maxWebfetchBytes)
	body, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("webfetch: read body: %w", err)
	}

	truncated := ""
	if len(body) >= maxWebfetchBytes {
		truncated = " (truncated at 1 MB)"
	}
	return fmt.Sprintf("%d%s\n\n%s", resp.StatusCode, truncated, string(body)), nil
}
