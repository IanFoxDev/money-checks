package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Notifier posts a message somewhere people will see it.
type Notifier interface {
	Notify(ctx context.Context, msg string) error
}

// Slack posts to an incoming webhook. Messages carry check names, counts and
// totals only, never rows, so nothing personal ends up in a channel.
type Slack struct {
	URL    string
	Client *http.Client
}

// NewSlack uses a client with a timeout, so a slow Slack cannot hold up a run.
func NewSlack(url string) *Slack {
	return &Slack{URL: url, Client: &http.Client{Timeout: 10 * time.Second}}
}

// Notify posts msg as the text of a message.
func (s *Slack) Notify(ctx context.Context, msg string) error {
	body, err := json.Marshal(map[string]string{"text": msg})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.Client.Do(req)
	if err != nil {
		// *url.Error prints the request URL, and the webhook URL is the secret.
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("slack: %w", ue.Err)
		}
		return fmt.Errorf("slack: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("slack: %s: %s", resp.Status, bytes.TrimSpace(b))
	}
	return nil
}
