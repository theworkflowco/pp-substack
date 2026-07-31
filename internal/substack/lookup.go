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

// Substack's private management feeds reject limits above the browser's value.
const feedPageSize = 10

var correlationMarkerPattern = regexp.MustCompile(
	`gtme-issue:[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`,
)

type Post struct {
	PostID            string  `json:"post_id"`
	PostURL           string  `json:"post_url"`
	Status            string  `json:"status"`
	ScheduledAt       *string `json:"scheduled_at"`
	PublishedAt       *string `json:"published_at"`
	DraftUpdatedAt    *string `json:"draft_updated_at"`
	CorrelationMarker string  `json:"correlation_marker"`
}

type Found struct {
	Found bool  `json:"found"`
	Post  *Post `json:"post,omitempty"`
}

type rawPost struct {
	ID             json.RawMessage `json:"id"`
	DraftBody      string          `json:"draft_body"`
	DraftUpdatedAt *string         `json:"draft_updated_at"`
	BodyHTML       string          `json:"body_html"`
	BodyJSON       json.RawMessage `json:"body_json"`
	Body           string          `json:"body"`
	PostDate       *string         `json:"post_date"`
	CanonicalURL   *string         `json:"canonical_url"`
	Slug           *string         `json:"slug"`
	TriggerAt      *string         `json:"trigger_at"`
	PostSchedules  []struct {
		TriggerAt *string `json:"trigger_at"`
	} `json:"postSchedules"`
}

type feed struct {
	Posts    *[]rawPost `json:"posts"`
	Total    *int       `json:"total"`
	Limit    *int       `json:"limit"`
	Offset   *int       `json:"offset"`
	IsCapped *bool      `json:"isCapped"`
}

type feedKind struct {
	Path      string
	OrderBy   string
	Direction string
	Status    string
}

type candidateEvidence struct {
	Raw            rawPost
	ExpectedStatus string
}

func (client *Client) FindByMarker(
	ctx context.Context,
	correlationMarker string,
) (Found, error) {
	if err := ValidateCorrelationMarker(correlationMarker); err != nil {
		return Found{}, err
	}

	kinds := []feedKind{
		{
			Path:      "/api/v1/post_management/drafts",
			OrderBy:   "draft_updated_at",
			Direction: "desc",
			Status:    "draft",
		},
		{
			Path:      "/api/v1/post_management/scheduled",
			OrderBy:   "trigger_at",
			Direction: "asc",
			Status:    "scheduled",
		},
		{
			Path:      "/api/v1/post_management/published",
			OrderBy:   "post_date",
			Direction: "desc",
			Status:    "published",
		},
	}

	candidatesByID := make(map[string]candidateEvidence)
	for _, kind := range kinds {
		candidates, err := client.listFeed(ctx, kind)
		if err != nil {
			return Found{}, fmt.Errorf("search Substack posts: %w", err)
		}
		for _, candidate := range candidates {
			if err := validateFeedLifecycle(candidate, kind.Status); err != nil {
				return Found{}, fmt.Errorf("search Substack posts: %w", err)
			}
			id, err := parseID(candidate.ID)
			if err != nil {
				return Found{}, fmt.Errorf("search Substack posts: feed row %w", err)
			}
			evidence := candidatesByID[id]
			if len(evidence.Raw.ID) == 0 {
				evidence.Raw.ID = candidate.ID
			}
			if candidate.TriggerAt != nil {
				evidence.Raw.TriggerAt = candidate.TriggerAt
			}
			if candidate.PostDate != nil {
				evidence.Raw.PostDate = candidate.PostDate
			}
			if lifecycleRank(kind.Status) > lifecycleRank(evidence.ExpectedStatus) {
				evidence.ExpectedStatus = kind.Status
			}
			candidatesByID[id] = evidence
		}
	}

	candidateIDs := make([]string, 0, len(candidatesByID))
	for id := range candidatesByID {
		candidateIDs = append(candidateIDs, id)
	}
	sort.Strings(candidateIDs)

	matches := make(map[string]Post)
	for _, id := range candidateIDs {
		evidence := candidatesByID[id]
		detail, err := client.fetchCandidate(
			ctx,
			id,
			evidence.ExpectedStatus == "published",
		)
		if err != nil {
			return Found{}, fmt.Errorf("search Substack posts: %w", err)
		}
		detailID, err := parseID(detail.ID)
		if err != nil {
			return Found{}, fmt.Errorf(
				"search Substack posts: candidate %s detail %w",
				id,
				err,
			)
		}
		if detailID != id {
			return Found{}, fmt.Errorf(
				"search Substack posts: detail id %q does not match candidate id %q",
				detailID,
				id,
			)
		}
		if detail.TriggerAt == nil {
			detail.TriggerAt = evidence.Raw.TriggerAt
		}
		if detail.PostDate == nil {
			detail.PostDate = evidence.Raw.PostDate
		}
		if err := validateExpectedLifecycle(detail, evidence.ExpectedStatus); err != nil {
			return Found{}, fmt.Errorf(
				"search Substack posts: candidate %s: %w",
				id,
				err,
			)
		}
		if !hasBody(detail) {
			return Found{}, fmt.Errorf(
				"search Substack posts: candidate %s detail is missing body",
				id,
			)
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

func validateFeedLifecycle(raw rawPost, status string) error {
	switch status {
	case "draft":
		return nil
	case "scheduled":
		if raw.TriggerAt == nil || *raw.TriggerAt == "" {
			return fmt.Errorf("scheduled feed row is missing trigger_at")
		}
		if err := validateRFC3339(*raw.TriggerAt, "scheduled feed trigger_at"); err != nil {
			return err
		}
		return nil
	case "published":
		if raw.PostDate == nil || *raw.PostDate == "" {
			return fmt.Errorf("published feed row is missing post_date")
		}
		if err := validateRFC3339(*raw.PostDate, "published feed post_date"); err != nil {
			return err
		}
		return nil
	default:
		panic("unknown lifecycle status")
	}
}

func validateExpectedLifecycle(raw rawPost, expectedStatus string) error {
	switch expectedStatus {
	case "draft":
		return nil
	case "scheduled":
		if raw.PostDate != nil && *raw.PostDate != "" {
			return nil
		}
		if scheduleTime(raw) == nil {
			return fmt.Errorf("scheduled candidate has no scheduled timestamp")
		}
		return nil
	case "published":
		if raw.PostDate == nil || *raw.PostDate == "" {
			return fmt.Errorf("published candidate has no published timestamp")
		}
		return nil
	default:
		panic("unknown lifecycle status")
	}
}

func lifecycleRank(status string) int {
	switch status {
	case "":
		return 0
	case "draft":
		return 1
	case "scheduled":
		return 2
	case "published":
		return 3
	default:
		panic("unknown lifecycle status")
	}
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
					if lifecycleErr := validateFeedLifecycle(
						candidate,
						"scheduled",
					); lifecycleErr != nil {
						return Found{}, fmt.Errorf(
							"get Substack post: %w",
							lifecycleErr,
						)
					}
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

	published, err := client.fetchPublishedPost(ctx, postID)
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
	offset := 0
	var expectedTotal *int
	for {
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
		if err := validateFeedPage(page, kind.Path, offset); err != nil {
			return nil, err
		}
		if *page.IsCapped {
			return nil, fmt.Errorf("%s response was capped", kind.Path)
		}
		if expectedTotal == nil {
			expectedTotal = page.Total
		} else if *page.Total != *expectedTotal {
			return nil, fmt.Errorf(
				"%s total changed during pagination from %d to %d",
				kind.Path,
				*expectedTotal,
				*page.Total,
			)
		}
		all = append(all, (*page.Posts)...)
		retrieved := *page.Offset + len(*page.Posts)
		if retrieved >= *page.Total {
			return all, nil
		}
		if len(*page.Posts) == 0 || len(*page.Posts) < *page.Limit {
			return nil, fmt.Errorf(
				"%s pagination stopped before total %d",
				kind.Path,
				*page.Total,
			)
		}
		offset = *page.Offset + *page.Limit
	}
}

func validateFeedPage(page feed, path string, requestedOffset int) error {
	if page.Posts == nil ||
		page.Total == nil ||
		page.Limit == nil ||
		page.Offset == nil ||
		page.IsCapped == nil {
		return fmt.Errorf(
			"%s response is missing required fields posts, total, limit, offset, or isCapped",
			path,
		)
	}
	if *page.Total < 0 {
		return fmt.Errorf("%s response total must not be negative", path)
	}
	if *page.Limit <= 0 {
		return fmt.Errorf("%s response limit must be positive", path)
	}
	if *page.Offset != requestedOffset {
		return fmt.Errorf(
			"%s response offset %d does not match requested offset %d",
			path,
			*page.Offset,
			requestedOffset,
		)
	}
	if len(*page.Posts) > *page.Limit {
		return fmt.Errorf(
			"%s response returned %d posts above its limit %d",
			path,
			len(*page.Posts),
			*page.Limit,
		)
	}
	return nil
}

func (client *Client) fetchCandidate(
	ctx context.Context,
	postID string,
	published bool,
) (rawPost, error) {
	path := "/api/v1/drafts/" + url.PathEscape(postID)
	if published {
		return client.fetchPublishedPost(ctx, postID)
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

func (client *Client) fetchPublishedPost(
	ctx context.Context,
	postID string,
) (rawPost, error) {
	var response struct {
		Post        *rawPost `json:"post"`
		Publication *struct {
			Hostname string `json:"hostname"`
		} `json:"publication"`
	}
	if err := client.requestJSON(
		ctx,
		http.MethodGet,
		client.accountBaseURL+"/api/v1/posts/by-id/"+url.PathEscape(postID),
		nil,
		&response,
	); err != nil {
		return rawPost{}, err
	}
	if response.Post == nil {
		return rawPost{}, fmt.Errorf(
			"published post response is missing post envelope",
		)
	}
	id, err := parseID(response.Post.ID)
	if err != nil {
		return rawPost{}, fmt.Errorf("published post response %w", err)
	}
	if id != postID {
		return rawPost{}, fmt.Errorf(
			"published post response id %q does not match requested id %q",
			id,
			postID,
		)
	}
	if response.Publication == nil || response.Publication.Hostname == "" {
		return rawPost{}, fmt.Errorf(
			"published post response is missing publication hostname",
		)
	}
	if !strings.EqualFold(response.Publication.Hostname, client.publicationHost) {
		return rawPost{}, fmt.Errorf(
			"published post response publication hostname %q does not match %q",
			response.Publication.Hostname,
			client.publicationHost,
		)
	}
	return *response.Post, nil
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
	postURL := client.publicationBaseURL + "/publish/post/" + url.PathEscape(id)
	var scheduledAt *string
	var publishedAt *string
	if raw.PostDate != nil && *raw.PostDate != "" {
		if err := validateRFC3339(*raw.PostDate, "post_date"); err != nil {
			return Post{}, err
		}
		status = "published"
		publishedAt = raw.PostDate
		postURL, err = client.publishedReaderURL(raw.CanonicalURL, raw.Slug)
		if err != nil {
			return Post{}, err
		}
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
	var draftUpdatedAt *string
	if status != "published" && raw.DraftBody != "" {
		if raw.DraftUpdatedAt == nil || strings.TrimSpace(*raw.DraftUpdatedAt) == "" {
			return Post{}, fmt.Errorf("draft_updated_at is required for draft content")
		}
		if err := validateRFC3339(*raw.DraftUpdatedAt, "draft_updated_at"); err != nil {
			return Post{}, err
		}
		draftUpdatedAt = raw.DraftUpdatedAt
	}

	return Post{
		PostID:            id,
		PostURL:           postURL,
		Status:            status,
		ScheduledAt:       scheduledAt,
		PublishedAt:       publishedAt,
		DraftUpdatedAt:    draftUpdatedAt,
		CorrelationMarker: marker,
	}, nil
}

func (client *Client) publishedReaderURL(
	canonicalURL *string,
	slug *string,
) (string, error) {
	if canonicalURL == nil || strings.TrimSpace(*canonicalURL) == "" {
		if slug == nil ||
			*slug == "" ||
			*slug != strings.TrimSpace(*slug) ||
			strings.ContainsAny(*slug, "/?#") {
			return "", fmt.Errorf(
				"published post is missing canonical_url and a safe slug",
			)
		}
		return (&url.URL{
			Scheme: "https",
			Host:   client.publicationHost,
			Path:   "/p/" + *slug,
		}).String(), nil
	}
	parsed, err := url.Parse(*canonicalURL)
	if err != nil ||
		parsed.Host == "" ||
		parsed.User != nil {
		return "", fmt.Errorf("published post canonical_url is not a safe HTTP(S) URL")
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("published post canonical_url must use HTTPS")
	}
	if !strings.EqualFold(parsed.Host, client.publicationHost) {
		return "", fmt.Errorf(
			"published post canonical_url host %q does not match %q",
			parsed.Host,
			client.publicationHost,
		)
	}
	if !strings.HasPrefix(parsed.Path, "/p/") ||
		strings.Trim(strings.TrimPrefix(parsed.Path, "/p/"), "/") == "" {
		return "", fmt.Errorf(
			"published post canonical_url is not a public reader URL",
		)
	}
	return parsed.String(), nil
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

func hasBody(raw rawPost) bool {
	for _, source := range rawBodySources(raw) {
		if strings.TrimSpace(source) != "" {
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

func ValidateCorrelationMarker(correlationMarker string) error {
	if correlationMarkerPattern.FindString(correlationMarker) != correlationMarker {
		return fmt.Errorf("correlation marker must be gtme-issue:<uuid>")
	}
	return nil
}
