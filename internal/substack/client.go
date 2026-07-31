package substack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

type UpdatedDraft struct {
	PostID            string `json:"post_id"`
	DraftURL          string `json:"draft_url"`
	Status            string `json:"status"`
	CorrelationMarker string `json:"correlation_marker"`
}

type draftLifecycle struct {
	IsPublished   *bool              `json:"is_published"`
	PostDate      json.RawMessage    `json:"post_date"`
	EmailSentAt   json.RawMessage    `json:"email_sent_at"`
	PostSchedules *[]json.RawMessage `json:"postSchedules"`
}

type draftByline struct {
	ID      json.Number `json:"id"`
	IsGuest *bool       `json:"is_guest"`
}

type refreshedDraft struct {
	draftLifecycle
	ID             json.RawMessage `json:"id"`
	DraftTitle     *string         `json:"draft_title"`
	DraftBody      *string         `json:"draft_body"`
	DraftUpdatedAt *string         `json:"draft_updated_at"`
	DraftBylines   *[]draftByline  `json:"draftBylines"`
	DraftBylinesV1 *[]draftByline  `json:"draft_bylines"`
}

type DraftComparison struct {
	PostID            string `json:"post_id"`
	Status            string `json:"status"`
	Matches           bool   `json:"matches"`
	TitleMatches      bool   `json:"title_matches"`
	BodyMatches       bool   `json:"body_matches"`
	DraftUpdatedAt    string `json:"draft_updated_at"`
	CorrelationMarker string `json:"correlation_marker"`
}

type HTTPError struct {
	StatusCode int
	Path       string
}

func (err *HTTPError) Error() string {
	return fmt.Sprintf("Substack returned HTTP %d at %s", err.StatusCode, err.Path)
}

type UpdateStage string

const (
	UpdateStagePreMutation              UpdateStage = "pre_mutation"
	UpdateStageMutationUnknown          UpdateStage = "mutation_unknown"
	UpdateStagePostMutationVerification UpdateStage = "post_mutation_verification"
)

type UpdateError struct {
	Stage              UpdateStage
	Code               string
	MutationDispatched bool
	Cause              error
}

type draftRefreshError struct {
	Code  string
	Cause error
}

func (err *draftRefreshError) Error() string { return err.Cause.Error() }

func (err *draftRefreshError) Unwrap() error { return err.Cause }

func invalidDraftRefresh(code string, cause error) error {
	return &draftRefreshError{Code: code, Cause: cause}
}

func (err *UpdateError) Error() string {
	return err.Cause.Error()
}

func (err *UpdateError) Unwrap() error {
	return err.Cause
}

func updateError(
	stage UpdateStage,
	code string,
	mutationDispatched bool,
	cause error,
) error {
	return &UpdateError{
		Stage:              stage,
		Code:               code,
		MutationDispatched: mutationDispatched,
		Cause:              cause,
	}
}

type Client struct {
	publicationBaseURL string
	publicationHost    string
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
		publicationHost:    publication.Host,
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
	if err := ValidateCorrelationMarker(correlationMarker); err != nil {
		return Draft{}, err
	}
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

func (client *Client) UpdateDraft(
	ctx context.Context,
	postID string,
	title string,
	proseMirrorBody string,
	correlationMarker string,
) (UpdatedDraft, error) {
	if strings.TrimSpace(postID) == "" {
		return UpdatedDraft{}, updateError(
			UpdateStagePreMutation,
			"draft_input_invalid",
			false,
			fmt.Errorf("post id is required"),
		)
	}
	if strings.TrimSpace(title) == "" {
		return UpdatedDraft{}, updateError(
			UpdateStagePreMutation,
			"draft_input_invalid",
			false,
			fmt.Errorf("title is required"),
		)
	}
	if err := ValidateCorrelationMarker(correlationMarker); err != nil {
		return UpdatedDraft{}, updateError(
			UpdateStagePreMutation,
			"draft_input_invalid",
			false,
			err,
		)
	}
	if strings.Count(proseMirrorBody, correlationMarker) != 1 {
		return UpdatedDraft{}, updateError(
			UpdateStagePreMutation,
			"draft_input_invalid",
			false,
			fmt.Errorf("prose mirror body must contain correlation marker exactly once"),
		)
	}

	found, err := client.GetPost(ctx, postID)
	if err != nil {
		return UpdatedDraft{}, updateError(
			UpdateStagePreMutation,
			"draft_lookup_failed",
			false,
			fmt.Errorf("update Substack draft: %w", err),
		)
	}
	if !found.Found || found.Post == nil {
		return UpdatedDraft{}, updateError(
			UpdateStagePreMutation,
			"draft_ownership_failed",
			false,
			fmt.Errorf("update Substack draft: post %q was not found", postID),
		)
	}
	if found.Post.Status != "draft" {
		return UpdatedDraft{}, updateError(
			UpdateStagePreMutation,
			"draft_ownership_failed",
			false,
			fmt.Errorf(
				"update Substack draft: post %q has status %q, want draft",
				postID,
				found.Post.Status,
			),
		)
	}
	if found.Post.CorrelationMarker != correlationMarker {
		return UpdatedDraft{}, updateError(
			UpdateStagePreMutation,
			"draft_ownership_failed",
			false,
			fmt.Errorf(
				"update Substack draft: post %q correlation marker does not match",
				postID,
			),
		)
	}

	current, err := client.refreshDraft(ctx, postID, correlationMarker)
	if err != nil {
		code := "draft_refresh_invalid"
		var refreshErr *draftRefreshError
		var httpErr *HTTPError
		if errors.As(err, &refreshErr) {
			code = refreshErr.Code
		} else if errors.As(err, &httpErr) {
			code = "draft_refresh_failed"
		}
		return UpdatedDraft{}, updateError(
			UpdateStagePreMutation,
			code,
			false,
			fmt.Errorf("update Substack draft: %w", err),
		)
	}
	endpoint := client.publicationBaseURL + "/api/v1/drafts/" +
		url.PathEscape(postID)

	type updateByline struct {
		ID      json.Number `json:"id"`
		IsGuest bool        `json:"is_guest"`
	}
	bylines := make([]updateByline, len(*current.DraftBylines))
	for index, byline := range *current.DraftBylines {
		if byline.ID == "" {
			return UpdatedDraft{}, updateError(
				UpdateStagePreMutation,
				"draft_refresh_invalid",
				false,
				fmt.Errorf("update Substack draft: refreshed response draft byline %d is missing id", index),
			)
		}
		if _, err := strconv.ParseUint(byline.ID.String(), 10, 64); err != nil {
			return UpdatedDraft{}, updateError(
				UpdateStagePreMutation,
				"draft_refresh_invalid",
				false,
				fmt.Errorf(
					"update Substack draft: refreshed response draft byline %d has invalid id: %w",
					index,
					err,
				),
			)
		}
		if byline.IsGuest == nil {
			return UpdatedDraft{}, updateError(
				UpdateStagePreMutation,
				"draft_refresh_invalid",
				false,
				fmt.Errorf("update Substack draft: refreshed response draft byline %d is missing is_guest", index),
			)
		}
		bylines[index] = updateByline{
			ID:      byline.ID,
			IsGuest: *byline.IsGuest,
		}
	}

	payload := struct {
		DetectLanguage       bool           `json:"detect_language"`
		DraftBody            string         `json:"draft_body"`
		DraftBylines         []updateByline `json:"draft_bylines"`
		DraftPodcastDuration *string        `json:"draft_podcast_duration"`
		DraftPodcastURL      *string        `json:"draft_podcast_url"`
		DraftSectionID       *string        `json:"draft_section_id"`
		DraftSubtitle        string         `json:"draft_subtitle"`
		DraftTitle           string         `json:"draft_title"`
		LastUpdatedAt        string         `json:"last_updated_at"`
		SectionChosen        bool           `json:"section_chosen"`
		Translations         []struct{}     `json:"translations"`
	}{
		DetectLanguage: true,
		DraftBody:      proseMirrorBody,
		DraftBylines:   bylines,
		DraftSubtitle:  "",
		DraftTitle:     title,
		LastUpdatedAt:  *current.DraftUpdatedAt,
		SectionChosen:  false,
		Translations:   []struct{}{},
	}
	var response struct {
		draftLifecycle
		ID        json.RawMessage `json:"id"`
		DraftBody *string         `json:"draft_body"`
	}
	mutationDispatched := true
	if err := client.requestJSON(
		ctx,
		http.MethodPut,
		endpoint,
		payload,
		&response,
	); err != nil {
		return UpdatedDraft{}, updateError(
			UpdateStageMutationUnknown,
			"update_transport_failed",
			mutationDispatched,
			fmt.Errorf("update Substack draft: %w", err),
		)
	}
	responseID, err := parseID(response.ID)
	if err != nil {
		return UpdatedDraft{}, updateError(
			UpdateStagePostMutationVerification,
			"update_response_invalid",
			mutationDispatched,
			fmt.Errorf("update Substack draft response: %w", err),
		)
	}
	if responseID != postID {
		return UpdatedDraft{}, updateError(
			UpdateStagePostMutationVerification,
			"update_response_invalid",
			mutationDispatched,
			fmt.Errorf(
				"update Substack draft response id %q does not match requested id %q",
				responseID,
				postID,
			),
		)
	}
	if err := validateDraftLifecycle(
		"update Substack draft response",
		response.draftLifecycle,
	); err != nil {
		return UpdatedDraft{}, updateError(
			UpdateStagePostMutationVerification,
			"update_response_invalid",
			mutationDispatched,
			err,
		)
	}
	if response.DraftBody == nil {
		return UpdatedDraft{}, updateError(
			UpdateStagePostMutationVerification,
			"update_response_invalid",
			mutationDispatched,
			fmt.Errorf("update Substack draft response is missing draft_body"),
		)
	}
	if strings.Count(*response.DraftBody, correlationMarker) != 1 {
		return UpdatedDraft{}, updateError(
			UpdateStagePostMutationVerification,
			"update_response_invalid",
			mutationDispatched,
			fmt.Errorf("update Substack draft response: correlation marker did not round-trip exactly once"),
		)
	}

	return UpdatedDraft{
		PostID: responseID,
		DraftURL: client.publicationBaseURL + "/publish/post/" +
			url.PathEscape(responseID),
		Status:            "draft",
		CorrelationMarker: correlationMarker,
	}, nil
}

func (client *Client) CompareDraft(
	ctx context.Context,
	postID string,
	title string,
	proseMirrorBody string,
	correlationMarker string,
) (DraftComparison, error) {
	if strings.TrimSpace(postID) == "" {
		return DraftComparison{}, fmt.Errorf("post id is required")
	}
	if strings.TrimSpace(title) == "" {
		return DraftComparison{}, fmt.Errorf("title is required")
	}
	if err := ValidateCorrelationMarker(correlationMarker); err != nil {
		return DraftComparison{}, err
	}
	if strings.Count(proseMirrorBody, correlationMarker) != 1 {
		return DraftComparison{}, fmt.Errorf(
			"prose mirror body must contain correlation marker exactly once",
		)
	}

	found, err := client.GetPost(ctx, postID)
	if err != nil {
		return DraftComparison{}, fmt.Errorf("compare Substack draft: %w", err)
	}
	if !found.Found || found.Post == nil {
		return DraftComparison{}, fmt.Errorf(
			"compare Substack draft: post %q was not found",
			postID,
		)
	}
	if found.Post.Status != "draft" {
		return DraftComparison{}, fmt.Errorf(
			"compare Substack draft: post %q has status %q, want draft",
			postID,
			found.Post.Status,
		)
	}
	if found.Post.CorrelationMarker != correlationMarker {
		return DraftComparison{}, fmt.Errorf(
			"compare Substack draft: post %q correlation marker does not match",
			postID,
		)
	}

	current, err := client.refreshDraft(ctx, postID, correlationMarker)
	if err != nil {
		return DraftComparison{}, fmt.Errorf("compare Substack draft: %w", err)
	}
	if current.DraftTitle == nil {
		return DraftComparison{}, fmt.Errorf(
			"compare Substack draft: refreshed response is missing draft_title",
		)
	}
	titleMatches := *current.DraftTitle == title
	bodyMatches := *current.DraftBody == proseMirrorBody
	return DraftComparison{
		PostID:            postID,
		Status:            "draft",
		Matches:           titleMatches && bodyMatches,
		TitleMatches:      titleMatches,
		BodyMatches:       bodyMatches,
		DraftUpdatedAt:    *current.DraftUpdatedAt,
		CorrelationMarker: correlationMarker,
	}, nil
}

func (client *Client) refreshDraft(
	ctx context.Context,
	postID string,
	correlationMarker string,
) (refreshedDraft, error) {
	endpoint := client.publicationBaseURL + "/api/v1/drafts/" +
		url.PathEscape(postID)
	var current refreshedDraft
	if err := client.requestJSON(
		ctx,
		http.MethodGet,
		endpoint,
		nil,
		&current,
	); err != nil {
		return refreshedDraft{}, fmt.Errorf("refresh draft before mutation: %w", err)
	}
	currentID, err := parseID(current.ID)
	if err != nil {
		return refreshedDraft{}, invalidDraftRefresh(
			"draft_refresh_identity_invalid",
			fmt.Errorf("refreshed response %w", err),
		)
	}
	if currentID != postID {
		return refreshedDraft{}, invalidDraftRefresh(
			"draft_refresh_identity_invalid",
			fmt.Errorf(
				"refreshed response id %q does not match requested id %q",
				currentID,
				postID,
			),
		)
	}
	if err := validateDraftLifecycle("refreshed response", current.draftLifecycle); err != nil {
		return refreshedDraft{}, invalidDraftRefresh(
			"draft_refresh_lifecycle_invalid",
			err,
		)
	}
	if current.DraftUpdatedAt == nil || strings.TrimSpace(*current.DraftUpdatedAt) == "" {
		return refreshedDraft{}, invalidDraftRefresh(
			"draft_refresh_updated_at_invalid",
			fmt.Errorf("refreshed response is missing draft_updated_at"),
		)
	}
	if err := validateRFC3339(*current.DraftUpdatedAt, "draft_updated_at"); err != nil {
		return refreshedDraft{}, invalidDraftRefresh(
			"draft_refresh_updated_at_invalid",
			fmt.Errorf("refreshed response %w", err),
		)
	}
	if current.DraftBody == nil {
		return refreshedDraft{}, invalidDraftRefresh(
			"draft_refresh_body_invalid",
			fmt.Errorf(
				"refreshed response is missing draft_body needed to verify correlation marker",
			),
		)
	}
	if strings.Count(*current.DraftBody, correlationMarker) != 1 {
		return refreshedDraft{}, invalidDraftRefresh(
			"draft_refresh_marker_invalid",
			fmt.Errorf(
				"refreshed response correlation marker is not present exactly once",
			),
		)
	}
	if err := normalizeDraftBylines(&current); err != nil {
		return refreshedDraft{}, invalidDraftRefresh(
			"draft_refresh_bylines_invalid",
			err,
		)
	}
	if current.DraftBylines == nil || len(*current.DraftBylines) == 0 {
		return refreshedDraft{}, invalidDraftRefresh(
			"draft_refresh_bylines_invalid",
			fmt.Errorf("refreshed response has no draft bylines"),
		)
	}
	return current, nil
}

func normalizeDraftBylines(current *refreshedDraft) error {
	if current.DraftBylines == nil {
		current.DraftBylines = current.DraftBylinesV1
		return nil
	}
	if current.DraftBylinesV1 == nil {
		return nil
	}
	if !equalDraftBylines(*current.DraftBylines, *current.DraftBylinesV1) {
		return fmt.Errorf(
			"refreshed response contains conflicting draft byline representations",
		)
	}
	return nil
}

func equalDraftBylines(left []draftByline, right []draftByline) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID ||
			(left[index].IsGuest == nil) != (right[index].IsGuest == nil) ||
			(left[index].IsGuest != nil &&
				*left[index].IsGuest != *right[index].IsGuest) {
			return false
		}
	}
	return true
}

func validateDraftLifecycle(context string, lifecycle draftLifecycle) error {
	if lifecycle.IsPublished == nil {
		return fmt.Errorf("%s is missing is_published", context)
	}
	if len(lifecycle.PostDate) == 0 {
		return fmt.Errorf("%s is missing post_date", context)
	}
	if len(lifecycle.EmailSentAt) == 0 {
		return fmt.Errorf("%s is missing email_sent_at", context)
	}
	if lifecycle.PostSchedules == nil {
		return fmt.Errorf("%s is missing postSchedules", context)
	}
	if *lifecycle.IsPublished ||
		!bytes.Equal(bytes.TrimSpace(lifecycle.PostDate), []byte("null")) {
		return fmt.Errorf("%s is published, not a draft", context)
	}
	if !bytes.Equal(bytes.TrimSpace(lifecycle.EmailSentAt), []byte("null")) {
		return fmt.Errorf("%s was sent, not a draft", context)
	}
	if len(*lifecycle.PostSchedules) > 0 {
		return fmt.Errorf("%s is scheduled, not an unscheduled draft", context)
	}
	return nil
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
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode JSON from %s: trailing JSON value", safePath(endpoint))
		}
		return fmt.Errorf("decode JSON from %s: trailing JSON data: %w", safePath(endpoint), err)
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
