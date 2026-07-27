package substack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const feedPageSize = 100

var correlationMarkerPattern = regexp.MustCompile(
	`gtme-issue:[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`,
)

type Post struct {
	PostID            string  `json:"post_id"`
	PostURL           string  `json:"post_url"`
	Status            string  `json:"status"`
	ScheduledAt       *string `json:"scheduled_at"`
	PublishedAt       *string `json:"published_at"`
	CorrelationMarker string  `json:"correlation_marker"`
}

type Found struct {
	Found bool  `json:"found"`
	Post  *Post `json:"post,omitempty"`
}

type rawPost struct {
	ID            json.RawMessage `json:"id"`
	DraftBody     string          `json:"draft_body"`
	BodyHTML      string          `json:"body_html"`
	BodyJSON      json.RawMessage `json:"body_json"`
	Body          string          `json:"body"`
	PostDate      *string         `json:"post_date"`
	TriggerAt     *string         `json:"trigger_at"`
	PostSchedules []struct {
		TriggerAt *string `json:"trigger_at"`
	} `json:"postSchedules"`
}

type feed struct {
	Posts    []rawPost `json:"posts"`
	Total    int       `json:"total"`
	Limit    int       `json:"limit"`
	Offset   int       `json:"offset"`
	IsCapped bool      `json:"isCapped"`
}

type feedKind struct {
	Path      string
	OrderBy   string
	Direction string
	Published bool
}

func (client *Client) FindByMarker(
	ctx context.Context,
	correlationMarker string,
) (Found, error) {
	if !correlationMarkerPattern.MatchString(correlationMarker) ||
		correlationMarkerPattern.FindString(correlationMarker) != correlationMarker {
		return Found{}, fmt.Errorf("correlation marker must be gtme-issue:<uuid>")
	}

	kinds := []feedKind{
		{
			Path:      "/api/v1/post_management/drafts",
			OrderBy:   "draft_updated_at",
			Direction: "desc",
		},
		{
			Path:      "/api/v1/post_management/scheduled",
			OrderBy:   "trigger_at",
			Direction: "asc",
		},
		{
			Path:      "/api/v1/post_management/published",
			OrderBy:   "post_date",
			Direction: "desc",
			Published: true,
		},
	}

	matches := make(map[string]Post)
	seen := make(map[string]struct{})
	for _, kind := range kinds {
		candidates, err := client.listFeed(ctx, kind)
		if err != nil {
			return Found{}, fmt.Errorf("search Substack posts: %w", err)
		}
		for _, candidate := range candidates {
			id, err := parseID(candidate.ID)
			if err != nil {
				return Found{}, fmt.Errorf("search Substack posts: feed row %w", err)
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}

			detail, err := client.fetchCandidate(ctx, id, kind.Published)
			if err != nil {
				return Found{}, fmt.Errorf("search Substack posts: %w", err)
			}
			if detail.TriggerAt == nil {
				detail.TriggerAt = candidate.TriggerAt
			}
			if detail.PostDate == nil {
				detail.PostDate = candidate.PostDate
			}
			if !rawContainsMarker(detail, correlationMarker) {
				continue
			}
			post, err := client.normalizePost(detail)
			if err != nil {
				return Found{}, fmt.Errorf("search Substack posts: %w", err)
			}
			matches[id] = post
		}
	}

	if len(matches) == 0 {
		return Found{Found: false}, nil
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for id := range matches {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return Found{}, fmt.Errorf(
			"correlation marker matches multiple posts: %s",
			strings.Join(ids, ", "),
		)
	}
	for _, post := range matches {
		return Found{Found: true, Post: &post}, nil
	}
	panic("unreachable")
}

func (client *Client) GetPost(ctx context.Context, postID string) (Found, error) {
	if strings.TrimSpace(postID) == "" {
		return Found{}, fmt.Errorf("post id is required")
	}

	var draft rawPost
	err := client.requestJSON(
		ctx,
		http.MethodGet,
		client.publicationBaseURL+"/api/v1/drafts/"+url.PathEscape(postID),
		nil,
		&draft,
	)
	if err == nil {
		if draft.PostDate == nil && scheduleTime(draft) == nil {
			scheduled, listErr := client.listFeed(ctx, feedKind{
				Path:      "/api/v1/post_management/scheduled",
				OrderBy:   "trigger_at",
				Direction: "asc",
			})
			if listErr != nil {
				return Found{}, fmt.Errorf("get Substack post: %w", listErr)
			}
			for _, candidate := range scheduled {
				candidateID, parseErr := parseID(candidate.ID)
				if parseErr != nil {
					return Found{}, fmt.Errorf(
						"get Substack post: scheduled feed row %w",
						parseErr,
					)
				}
				if candidateID == postID {
					draft.TriggerAt = candidate.TriggerAt
					break
				}
			}
		}
		post, normalizeErr := client.normalizePost(draft)
		if normalizeErr != nil {
			return Found{}, fmt.Errorf("get Substack post: %w", normalizeErr)
		}
		if post.PostID != postID {
			return Found{}, fmt.Errorf(
				"get Substack post: response id %q does not match requested id %q",
				post.PostID,
				postID,
			)
		}
		return Found{Found: true, Post: &post}, nil
	}
	if !isHTTPStatus(err, http.StatusNotFound) {
		return Found{}, fmt.Errorf("get Substack post: %w", err)
	}

	var published rawPost
	err = client.requestJSON(
		ctx,
		http.MethodGet,
		client.publicationBaseURL+"/api/v1/posts/by-id/"+url.PathEscape(postID),
		nil,
		&published,
	)
	if err == nil {
		post, normalizeErr := client.normalizePost(published)
		if normalizeErr != nil {
			return Found{}, fmt.Errorf("get Substack post: %w", normalizeErr)
		}
		if post.Status != "published" {
			return Found{}, fmt.Errorf(
				"get Substack post: published endpoint returned status %q",
				post.Status,
			)
		}
		if post.PostID != postID {
			return Found{}, fmt.Errorf(
				"get Substack post: response id %q does not match requested id %q",
				post.PostID,
				postID,
			)
		}
		return Found{Found: true, Post: &post}, nil
	}
	if isHTTPStatus(err, http.StatusNotFound) {
		return Found{Found: false}, nil
	}
	return Found{}, fmt.Errorf("get Substack post: %w", err)
}

func (client *Client) listFeed(ctx context.Context, kind feedKind) ([]rawPost, error) {
	all := make([]rawPost, 0)
	for offset := 0; ; offset += feedPageSize {
		query := url.Values{
			"offset":          {strconv.Itoa(offset)},
			"limit":           {strconv.Itoa(feedPageSize)},
			"order_by":        {kind.OrderBy},
			"order_direction": {kind.Direction},
		}
		var page feed
		if err := client.requestJSON(
			ctx,
			http.MethodGet,
			client.publicationBaseURL+kind.Path+"?"+query.Encode(),
			nil,
			&page,
		); err != nil {
			return nil, err
		}
		if page.IsCapped {
			return nil, fmt.Errorf("%s response was capped", kind.Path)
		}
		all = append(all, page.Posts...)
		retrieved := offset + len(page.Posts)
		if retrieved >= page.Total {
			return all, nil
		}
		if len(page.Posts) == 0 {
			return nil, fmt.Errorf(
				"%s pagination stopped before total %d",
				kind.Path,
				page.Total,
			)
		}
	}
}

func (client *Client) fetchCandidate(
	ctx context.Context,
	postID string,
	published bool,
) (rawPost, error) {
	path := "/api/v1/drafts/" + url.PathEscape(postID)
	if published {
		path = "/api/v1/posts/by-id/" + url.PathEscape(postID)
	}
	var result rawPost
	if err := client.requestJSON(
		ctx,
		http.MethodGet,
		client.publicationBaseURL+path,
		nil,
		&result,
	); err != nil {
		return rawPost{}, err
	}
	return result, nil
}

func (client *Client) normalizePost(raw rawPost) (Post, error) {
	id, err := parseID(raw.ID)
	if err != nil {
		return Post{}, fmt.Errorf("post response %w", err)
	}
	marker, err := extractCorrelationMarker(raw)
	if err != nil {
		return Post{}, fmt.Errorf("post %s: %w", id, err)
	}

	status := "draft"
	var scheduledAt *string
	var publishedAt *string
	if raw.PostDate != nil && *raw.PostDate != "" {
		if err := validateRFC3339(*raw.PostDate, "post_date"); err != nil {
			return Post{}, err
		}
		status = "published"
		publishedAt = raw.PostDate
		if schedule := scheduleTime(raw); schedule != nil {
			if err := validateRFC3339(*schedule, "scheduled time"); err != nil {
				return Post{}, err
			}
			scheduledAt = schedule
		}
	} else if schedule := scheduleTime(raw); schedule != nil {
		if err := validateRFC3339(*schedule, "scheduled time"); err != nil {
			return Post{}, err
		}
		status = "scheduled"
		scheduledAt = schedule
	}

	return Post{
		PostID:            id,
		PostURL:           client.publicationBaseURL + "/publish/post/" + url.PathEscape(id),
		Status:            status,
		ScheduledAt:       scheduledAt,
		PublishedAt:       publishedAt,
		CorrelationMarker: marker,
	}, nil
}

func scheduleTime(raw rawPost) *string {
	if raw.TriggerAt != nil && *raw.TriggerAt != "" {
		return raw.TriggerAt
	}
	for _, schedule := range raw.PostSchedules {
		if schedule.TriggerAt != nil && *schedule.TriggerAt != "" {
			return schedule.TriggerAt
		}
	}
	return nil
}

func rawContainsMarker(raw rawPost, marker string) bool {
	for _, source := range rawBodySources(raw) {
		if strings.Contains(source, marker) {
			return true
		}
	}
	return false
}

func extractCorrelationMarker(raw rawPost) (string, error) {
	markers := make(map[string]struct{})
	for _, source := range rawBodySources(raw) {
		for _, marker := range correlationMarkerPattern.FindAllString(source, -1) {
			markers[marker] = struct{}{}
		}
	}
	if len(markers) == 0 {
		return "", fmt.Errorf("response contains no correlation marker")
	}
	if len(markers) > 1 {
		return "", fmt.Errorf("response contains multiple correlation markers")
	}
	for marker := range markers {
		return marker, nil
	}
	panic("unreachable")
}

func rawBodySources(raw rawPost) []string {
	sources := []string{raw.DraftBody, raw.BodyHTML, raw.Body}
	if len(raw.BodyJSON) > 0 && string(raw.BodyJSON) != "null" {
		sources = append(sources, string(raw.BodyJSON))
	}
	return sources
}

func validateRFC3339(value string, field string) error {
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("%s is not RFC 3339: %w", field, err)
	}
	return nil
}

func isHTTPStatus(err error, status int) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == status
}
