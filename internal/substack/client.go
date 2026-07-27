package substack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const browserUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"

type Draft struct {
	DraftID           string `json:"draft_id"`
	DraftURL          string `json:"draft_url"`
	Status            string `json:"status"`
	CorrelationMarker string `json:"correlation_marker"`
}

type HTTPError struct {
	StatusCode int
	Path       string
}

func (err *HTTPError) Error() string {
	return fmt.Sprintf("Substack returned HTTP %d at %s", err.StatusCode, err.Path)
}

type Client struct {
	publicationBaseURL string
	accountBaseURL     string
	cookie             string
	httpClient         *http.Client
}

func NewClient(
	publicationBaseURL string,
	accountBaseURL string,
	cookie string,
	httpClient *http.Client,
) (*Client, error) {
	publication, err := parseBaseURL(publicationBaseURL)
	if err != nil {
		return nil, fmt.Errorf("publication URL: %w", err)
	}
	account, err := parseBaseURL(accountBaseURL)
	if err != nil {
		return nil, fmt.Errorf("account URL: %w", err)
	}
	if strings.TrimSpace(cookie) == "" {
		return nil, fmt.Errorf("PP_SUBSTACK_SESSION_COOKIE is required")
	}
	if httpClient == nil {
		return nil, fmt.Errorf("HTTP client is required")
	}
	return &Client{
		publicationBaseURL: strings.TrimRight(publication.String(), "/"),
		accountBaseURL:     strings.TrimRight(account.String(), "/"),
		cookie:             cookie,
		httpClient:         httpClient,
	}, nil
}

func (client *Client) CreateDraft(
	ctx context.Context,
	title string,
	proseMirrorBody string,
	correlationMarker string,
) (Draft, error) {
	var profile struct {
		ID json.Number `json:"id"`
	}
	if err := client.requestJSON(
		ctx,
		http.MethodGet,
		client.accountBaseURL+"/api/v1/user/profile/self",
		nil,
		&profile,
	); err != nil {
		return Draft{}, fmt.Errorf("resolve Substack byline: %w", err)
	}
	if profile.ID == "" {
		return Draft{}, fmt.Errorf("resolve Substack byline: response is missing id")
	}

	payload := map[string]any{
		"audience":        "everyone",
		"detect_language": true,
		"draft_body":      proseMirrorBody,
		"draft_bylines": []map[string]any{{
			"id":       profile.ID,
			"is_guest": false,
		}},
		"draft_podcast_duration": nil,
		"draft_podcast_url":      nil,
		"draft_section_id":       nil,
		"draft_subtitle":         "",
		"draft_title":            title,
		"section_chosen":         false,
		"translations":           []any{},
		"type":                   "newsletter",
	}
	var response struct {
		ID        json.RawMessage `json:"id"`
		DraftBody string          `json:"draft_body"`
	}
	if err := client.requestJSON(
		ctx,
		http.MethodPost,
		client.publicationBaseURL+"/api/v1/drafts",
		payload,
		&response,
	); err != nil {
		return Draft{}, fmt.Errorf("create Substack draft: %w", err)
	}
	id, err := parseID(response.ID)
	if err != nil {
		return Draft{}, fmt.Errorf("create Substack draft response: %w", err)
	}
	if strings.Count(response.DraftBody, correlationMarker) != 1 {
		return Draft{}, fmt.Errorf(
			"create Substack draft response: correlation marker did not round-trip exactly once",
		)
	}
	return Draft{
		DraftID:           id,
		DraftURL:          client.publicationBaseURL + "/publish/post/" + url.PathEscape(id),
		Status:            "draft",
		CorrelationMarker: correlationMarker,
	}, nil
}

func (client *Client) requestJSON(
	ctx context.Context,
	method string,
	endpoint string,
	body any,
	target any,
) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("Cookie", client.cookie)
	request.Header.Set("Referer", client.publicationBaseURL+"/publish/home")
	request.Header.Set("User-Agent", browserUserAgent)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("transport failure at %s: %w", safePath(endpoint), err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return &HTTPError{StatusCode: response.StatusCode, Path: safePath(endpoint)}
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 8<<20))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode JSON from %s: %w", safePath(endpoint), err)
	}
	return nil
}

func parseBaseURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("scheme must be HTTP or HTTPS")
	}
	if parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("must contain only scheme and host")
	}
	return parsed, nil
}

func parseID(value json.RawMessage) (string, error) {
	if len(value) == 0 {
		return "", fmt.Errorf("response is missing id")
	}
	var stringID string
	if err := json.Unmarshal(value, &stringID); err == nil {
		if stringID == "" {
			return "", fmt.Errorf("response contains an empty id")
		}
		return stringID, nil
	}
	var numberID json.Number
	if err := json.Unmarshal(value, &numberID); err != nil {
		return "", fmt.Errorf("response id is not a string or number")
	}
	if _, err := strconv.ParseUint(numberID.String(), 10, 64); err != nil {
		return "", fmt.Errorf("response id is not an unsigned integer")
	}
	return numberID.String(), nil
}

func safePath(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "Substack endpoint"
	}
	return parsed.EscapedPath()
}
