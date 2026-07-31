package substack_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/theworkflowco/pp-substack/internal/substack"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type observedUpdateDraftByline struct {
	ID      *int64 `json:"id"`
	IsGuest *bool  `json:"is_guest"`
}

type observedUpdateDraftRequest struct {
	DetectLanguage       *bool                        `json:"detect_language"`
	DraftBody            *string                      `json:"draft_body"`
	DraftBylines         *[]observedUpdateDraftByline `json:"draft_bylines"`
	DraftPodcastDuration json.RawMessage              `json:"draft_podcast_duration"`
	DraftPodcastURL      json.RawMessage              `json:"draft_podcast_url"`
	DraftSectionID       json.RawMessage              `json:"draft_section_id"`
	DraftSubtitle        *string                      `json:"draft_subtitle"`
	DraftTitle           *string                      `json:"draft_title"`
	LastUpdatedAt        *string                      `json:"last_updated_at"`
	SectionChosen        *bool                        `json:"section_chosen"`
	Translations         *[]struct{}                  `json:"translations"`
}

func TestUpdateDraftRejectsInvalidInputMarkerBeforeNetwork(t *testing.T) {
	t.Parallel()

	const marker = "gtme-issue:11111111-2222-4333-8444-555555555555"
	for _, test := range []struct {
		name string
		body string
	}{
		{
			name: "missing",
			body: proseMirrorWith(
				"gtme-issue:aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
			),
		},
		{
			name: "duplicated",
			body: proseMirrorWith(marker + " " + marker),
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var requestCount atomic.Int32
			var putCount atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				request *http.Request,
			) {
				requestCount.Add(1)
				if request.Method == http.MethodPut {
					putCount.Add(1)
				}
				writeJSON(t, response, http.StatusInternalServerError, map[string]any{})
			}))
			defer server.Close()

			client := mustClient(t, server, "connect.sid=synthetic-session")
			_, err := client.UpdateDraft(
				context.Background(),
				"42424242",
				"Synthetic update title",
				test.body,
				marker,
			)
			if err == nil || !strings.Contains(err.Error(), "correlation marker") {
				t.Fatalf(
					"UpdateDraft() error = %v, want input marker error",
					err,
				)
			}
			if requestCount.Load() != 0 {
				t.Errorf("request count = %d, want 0", requestCount.Load())
			}
			if putCount.Load() != 0 {
				t.Errorf("PUT count = %d, want 0", putCount.Load())
			}
		})
	}
}

func TestUpdateDraftRejectsChangedMarkerOnFinalRefresh(t *testing.T) {
	t.Parallel()

	const (
		postID = "42424242"
		marker = "gtme-issue:11111111-2222-4333-8444-555555555555"
	)
	for _, test := range []struct {
		name          string
		mutateRefresh func(map[string]any)
	}{
		{
			name: "missing body",
			mutateRefresh: func(refresh map[string]any) {
				delete(refresh, "draft_body")
			},
		},
		{
			name: "different marker",
			mutateRefresh: func(refresh map[string]any) {
				refresh["draft_body"] = proseMirrorWith(
					"gtme-issue:aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
				)
			},
		},
		{
			name: "duplicated marker",
			mutateRefresh: func(refresh map[string]any) {
				refresh["draft_body"] = proseMirrorWith(marker + " " + marker)
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server, putCount := updateDraftRefreshFailureServer(
				t,
				postID,
				marker,
				test.mutateRefresh,
			)
			defer server.Close()

			client := mustClient(t, server, "connect.sid=synthetic-session")
			_, err := client.UpdateDraft(
				context.Background(),
				postID,
				"Synthetic update title",
				proseMirrorWith(marker),
				marker,
			)
			if err == nil || !strings.Contains(err.Error(), "correlation marker") {
				t.Fatalf(
					"UpdateDraft() error = %v, want refreshed marker error",
					err,
				)
			}
			if putCount.Load() != 0 {
				t.Errorf("PUT count = %d, want 0", putCount.Load())
			}
		})
	}
}

func TestUpdateDraftRejectsMalformedRefreshTimestamp(t *testing.T) {
	t.Parallel()

	const (
		postID = "42424242"
		marker = "gtme-issue:11111111-2222-4333-8444-555555555555"
	)
	server, putCount := updateDraftRefreshFailureServer(
		t,
		postID,
		marker,
		func(refresh map[string]any) {
			refresh["draft_updated_at"] = "not-a-timestamp"
		},
	)
	defer server.Close()

	client := mustClient(t, server, "connect.sid=synthetic-session")
	_, err := client.UpdateDraft(
		context.Background(),
		postID,
		"Synthetic update title",
		proseMirrorWith(marker),
		marker,
	)
	if err == nil || !strings.Contains(err.Error(), "draft_updated_at") {
		t.Fatalf(
			"UpdateDraft() error = %v, want refreshed timestamp error",
			err,
		)
	}
	if putCount.Load() != 0 {
		t.Errorf("PUT count = %d, want 0", putCount.Load())
	}
}

func TestUpdateDraftReportsPreMutationStageEvidence(t *testing.T) {
	t.Parallel()

	const (
		postID = "42424242"
		marker = "gtme-issue:11111111-2222-4333-8444-555555555555"
	)
	server, putCount := updateDraftRefreshFailureServer(
		t,
		postID,
		marker,
		func(refresh map[string]any) {
			refresh["draft_updated_at"] = "not-a-timestamp"
		},
	)
	defer server.Close()

	client := mustClient(t, server, "connect.sid=synthetic-session")
	_, err := client.UpdateDraft(
		context.Background(),
		postID,
		"Synthetic update title",
		proseMirrorWith(marker),
		marker,
	)
	var updateErr *substack.UpdateError
	if !errors.As(err, &updateErr) {
		t.Fatalf("UpdateDraft() error = %v, want *substack.UpdateError", err)
	}
	if updateErr.Stage != substack.UpdateStagePreMutation ||
		updateErr.Code != "draft_refresh_invalid" ||
		updateErr.MutationDispatched {
		t.Fatalf("UpdateDraft() evidence = %#v", updateErr)
	}
	if putCount.Load() != 0 {
		t.Fatalf("PUT count = %d, want 0", putCount.Load())
	}
}

func TestUpdateDraftReportsMutationUnknownStageEvidence(t *testing.T) {
	t.Parallel()

	const (
		postID = "42424242"
		marker = "gtme-issue:11111111-2222-4333-8444-555555555555"
	)
	server := updateDraftServer(t, postID, marker, func(map[string]any) {})
	defer server.Close()

	baseTransport := server.Client().Transport
	httpClient := &http.Client{Transport: roundTripFunc(func(
		request *http.Request,
	) (*http.Response, error) {
		if request.Method == http.MethodPut {
			response, err := baseTransport.RoundTrip(request)
			if err != nil {
				return nil, err
			}
			response.Body.Close()
			return nil, fmt.Errorf("synthetic connection reset after dispatch")
		}
		return baseTransport.RoundTrip(request)
	})}
	client, err := substack.NewClient(
		server.URL,
		server.URL,
		"connect.sid=synthetic-session",
		httpClient,
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = client.UpdateDraft(
		context.Background(),
		postID,
		"Synthetic update title",
		proseMirrorWith(marker),
		marker,
	)
	var updateErr *substack.UpdateError
	if !errors.As(err, &updateErr) {
		t.Fatalf("UpdateDraft() error = %v, want *substack.UpdateError", err)
	}
	if updateErr.Stage != substack.UpdateStageMutationUnknown ||
		updateErr.Code != "update_transport_failed" ||
		!updateErr.MutationDispatched {
		t.Fatalf("UpdateDraft() evidence = %#v", updateErr)
	}
}

func TestUpdateDraftReportsPostMutationVerificationStageEvidence(t *testing.T) {
	t.Parallel()

	const (
		postID = "42424242"
		marker = "gtme-issue:11111111-2222-4333-8444-555555555555"
	)
	numericPostID := mustNumericPostID(t, postID)
	server := updateDraftServer(t, postID, marker, func(response map[string]any) {
		response["id"] = numericPostID + 1
	})
	defer server.Close()

	client := mustClient(t, server, "connect.sid=synthetic-session")
	_, err := client.UpdateDraft(
		context.Background(),
		postID,
		"Synthetic update title",
		proseMirrorWith(marker),
		marker,
	)
	var updateErr *substack.UpdateError
	if !errors.As(err, &updateErr) {
		t.Fatalf("UpdateDraft() error = %v, want *substack.UpdateError", err)
	}
	if updateErr.Stage != substack.UpdateStagePostMutationVerification ||
		updateErr.Code != "update_response_invalid" ||
		!updateErr.MutationDispatched {
		t.Fatalf("UpdateDraft() evidence = %#v", updateErr)
	}
}

func TestCompareDraftReportsExactTitleAndBodyMatchWithoutMutation(t *testing.T) {
	t.Parallel()

	const (
		postID   = "42424242"
		marker   = "gtme-issue:11111111-2222-4333-8444-555555555555"
		title    = "Synthetic intended title"
		updated  = "2026-07-31T14:00:00.000Z"
		bylineID = 10101
	)
	body := proseMirrorWith(marker)

	for _, test := range []struct {
		name         string
		currentTitle string
		currentBody  string
		titleMatches bool
		bodyMatches  bool
	}{
		{
			name:         "exact match",
			currentTitle: title,
			currentBody:  body,
			titleMatches: true,
			bodyMatches:  true,
		},
		{
			name:         "title mismatch",
			currentTitle: "Different title",
			currentBody:  body,
			titleMatches: false,
			bodyMatches:  true,
		},
		{
			name:         "body mismatch",
			currentTitle: title,
			currentBody:  proseMirrorWith(marker + " changed"),
			titleMatches: true,
			bodyMatches:  false,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var stage atomic.Int32
			var putCount atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				request *http.Request,
			) {
				if request.Method == http.MethodPut {
					putCount.Add(1)
					t.Errorf("comparison sent PUT %s", request.URL.Path)
				}
				switch {
				case request.Method == http.MethodGet &&
					request.URL.Path == "/api/v1/drafts/"+postID:
					switch {
					case stage.CompareAndSwap(0, 1):
						writeJSON(t, response, http.StatusOK, map[string]any{
							"id":               42424242,
							"draft_title":      test.currentTitle,
							"draft_body":       test.currentBody,
							"draft_updated_at": updated,
							"postSchedules":    []any{},
						})
					case stage.CompareAndSwap(2, 3):
						writeJSON(t, response, http.StatusOK, map[string]any{
							"id":               42424242,
							"draft_title":      test.currentTitle,
							"draft_body":       test.currentBody,
							"draft_updated_at": updated,
							"is_published":     false,
							"post_date":        nil,
							"email_sent_at":    nil,
							"postSchedules":    []any{},
							"draftBylines": []any{
								map[string]any{"id": bylineID, "is_guest": false},
							},
						})
					default:
						t.Fatalf("draft detail request at stage %d", stage.Load())
					}
				case request.Method == http.MethodGet &&
					request.URL.Path == "/api/v1/post_management/scheduled":
					if !stage.CompareAndSwap(1, 2) {
						t.Fatalf("scheduled feed request at stage %d", stage.Load())
					}
					writeJSON(t, response, http.StatusOK, emptyScheduledUpdateFeed())
				default:
					t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
				}
			}))
			defer server.Close()

			client := mustClient(t, server, "connect.sid=synthetic-session")
			result, err := client.CompareDraft(
				context.Background(),
				postID,
				title,
				body,
				marker,
			)
			if err != nil {
				t.Fatalf("CompareDraft() error = %v", err)
			}
			if result.PostID != postID || result.Status != "draft" ||
				result.TitleMatches != test.titleMatches ||
				result.BodyMatches != test.bodyMatches ||
				result.Matches != (test.titleMatches && test.bodyMatches) ||
				result.DraftUpdatedAt != updated ||
				result.CorrelationMarker != marker {
				t.Fatalf("CompareDraft() result = %#v", result)
			}
			if stage.Load() != 3 {
				t.Fatalf("request stage = %d, want three GETs", stage.Load())
			}
			if putCount.Load() != 0 {
				t.Fatalf("PUT count = %d, want 0", putCount.Load())
			}
		})
	}
}

func TestCompareDraftRejectsUnsafeLifecycleAndOwnershipWithoutMutation(t *testing.T) {
	t.Parallel()

	const (
		postID = "42424242"
		marker = "gtme-issue:11111111-2222-4333-8444-555555555555"
	)
	for _, test := range []struct {
		name        string
		remoteBody  string
		updatedAt   string
		schedules   []any
		wantMessage string
	}{
		{
			name: "marker mismatch",
			remoteBody: proseMirrorWith(
				"gtme-issue:aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
			),
			updatedAt:   "2026-07-31T14:00:00.000Z",
			schedules:   []any{},
			wantMessage: "correlation marker does not match",
		},
		{
			name:       "scheduled refusal",
			remoteBody: proseMirrorWith(marker),
			updatedAt:  "2026-07-31T14:00:00.000Z",
			schedules: []any{
				map[string]any{"trigger_at": "2026-08-01T14:00:00.000Z"},
			},
			wantMessage: "status \"scheduled\"",
		},
		{
			name:        "malformed update time",
			remoteBody:  proseMirrorWith(marker),
			updatedAt:   "not-a-timestamp",
			schedules:   []any{},
			wantMessage: "draft_updated_at",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var putCount atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				request *http.Request,
			) {
				if request.Method == http.MethodPut {
					putCount.Add(1)
					t.Errorf("comparison sent PUT %s", request.URL.Path)
				}
				switch request.URL.Path {
				case "/api/v1/drafts/" + postID:
					writeJSON(t, response, http.StatusOK, map[string]any{
						"id":               42424242,
						"draft_title":      "Current title",
						"draft_body":       test.remoteBody,
						"draft_updated_at": test.updatedAt,
						"postSchedules":    test.schedules,
					})
				case "/api/v1/post_management/scheduled":
					writeJSON(t, response, http.StatusOK, emptyScheduledUpdateFeed())
				default:
					t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
				}
			}))
			defer server.Close()

			client := mustClient(t, server, "connect.sid=synthetic-session")
			_, err := client.CompareDraft(
				context.Background(),
				postID,
				"Intended title",
				proseMirrorWith(marker),
				marker,
			)
			if err == nil || !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("CompareDraft() error = %v, want %q", err, test.wantMessage)
			}
			if putCount.Load() != 0 {
				t.Fatalf("PUT count = %d, want 0", putCount.Load())
			}
		})
	}
}

func TestUpdateDraftSendsObservedRequestAndReturnsDraft(t *testing.T) {
	t.Parallel()

	const (
		postID            = "42424242"
		title             = "Synthetic update title"
		marker            = "gtme-issue:11111111-2222-4333-8444-555555555555"
		previousUpdatedAt = "2026-07-28T14:00:00.000Z"
		syntheticBylineID = 10101
	)
	body := proseMirrorWith(marker)
	numericPostID := mustNumericPostID(t, postID)

	var stage atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == "/api/v1/drafts/"+postID:
			if request.URL.RawQuery != "" {
				t.Errorf("draft detail query = %q, want empty", request.URL.RawQuery)
			}
			switch {
			case stage.CompareAndSwap(0, 1):
				writeJSON(t, response, http.StatusOK, map[string]any{
					"id":               numericPostID,
					"draft_body":       body,
					"draft_updated_at": previousUpdatedAt,
					"postSchedules":    []any{},
				})
			case stage.CompareAndSwap(2, 3):
				writeJSON(t, response, http.StatusOK, map[string]any{
					"id":               numericPostID,
					"draft_title":      "Synthetic prior title",
					"draft_body":       body,
					"draft_updated_at": previousUpdatedAt,
					"is_published":     false,
					"post_date":        nil,
					"email_sent_at":    nil,
					"postSchedules":    []any{},
					"draftBylines": []any{
						map[string]any{
							"id":       syntheticBylineID,
							"is_guest": false,
						},
					},
				})
			default:
				t.Fatalf("draft detail request at stage %d", stage.Load())
			}
		case request.Method == http.MethodGet &&
			request.URL.Path == "/api/v1/post_management/scheduled":
			if request.URL.RawQuery != scheduledFeedQuery {
				t.Errorf(
					"scheduled feed query = %q, want %q",
					request.URL.RawQuery,
					scheduledFeedQuery,
				)
			}
			if !stage.CompareAndSwap(1, 2) {
				t.Fatalf("scheduled feed request at stage %d", stage.Load())
			}
			writeJSON(t, response, http.StatusOK, emptyScheduledUpdateFeed())
		case request.Method == http.MethodPut &&
			request.URL.Path == "/api/v1/drafts/"+postID:
			if request.URL.RawQuery != "" {
				t.Errorf("PUT query = %q, want empty", request.URL.RawQuery)
			}
			if !stage.CompareAndSwap(3, 4) {
				t.Errorf("PUT was not immediately preceded by the lifecycle recheck")
			}
			if request.Header.Get("Content-Type") != "application/json" {
				t.Errorf(
					"Content-Type = %q, want application/json",
					request.Header.Get("Content-Type"),
				)
			}
			decoder := json.NewDecoder(request.Body)
			decoder.DisallowUnknownFields()
			var payload observedUpdateDraftRequest
			if err := decoder.Decode(&payload); err != nil {
				t.Fatalf("decode strict PUT payload: %v", err)
			}
			var trailing json.RawMessage
			if err := decoder.Decode(&trailing); err != io.EOF {
				if err == nil {
					t.Fatal("decode strict PUT payload: trailing JSON value")
				}
				t.Fatalf("decode strict PUT payload: trailing JSON data: %v", err)
			}

			detectLanguage := true
			expectedBody := body
			bylineID := int64(syntheticBylineID)
			isGuest := false
			expectedBylines := []observedUpdateDraftByline{
				{
					ID:      &bylineID,
					IsGuest: &isGuest,
				},
			}
			draftSubtitle := ""
			expectedTitle := title
			lastUpdatedAt := previousUpdatedAt
			sectionChosen := false
			translations := []struct{}{}
			expected := observedUpdateDraftRequest{
				DetectLanguage:       &detectLanguage,
				DraftBody:            &expectedBody,
				DraftBylines:         &expectedBylines,
				DraftPodcastDuration: json.RawMessage("null"),
				DraftPodcastURL:      json.RawMessage("null"),
				DraftSectionID:       json.RawMessage("null"),
				DraftSubtitle:        &draftSubtitle,
				DraftTitle:           &expectedTitle,
				LastUpdatedAt:        &lastUpdatedAt,
				SectionChosen:        &sectionChosen,
				Translations:         &translations,
			}
			if !reflect.DeepEqual(payload, expected) {
				t.Errorf("PUT payload = %#v, want %#v", payload, expected)
			}
			writeJSON(
				t,
				response,
				http.StatusOK,
				loadUpdateDraftResponse(t),
			)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	client := mustClient(t, server, "connect.sid=synthetic-session")
	result, err := client.UpdateDraft(
		context.Background(),
		postID,
		title,
		body,
		marker,
	)
	if err != nil {
		t.Fatalf("UpdateDraft() error = %v", err)
	}
	if stage.Load() != 4 {
		t.Fatalf("request stage = %d, want safe four-request sequence", stage.Load())
	}
	assertUpdatedDraftReturned(
		t,
		result,
		postID,
		server.URL+"/publish/post/"+postID,
		marker,
	)
}

func TestUpdateDraftRefusesScheduledPostBeforeMutation(t *testing.T) {
	t.Parallel()

	const (
		postID = "42424242"
		marker = "gtme-issue:11111111-2222-4333-8444-555555555555"
	)
	var putCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == "/api/v1/drafts/"+postID:
			if request.URL.RawQuery != "" {
				t.Errorf("draft detail query = %q, want empty", request.URL.RawQuery)
			}
			writeJSON(t, response, http.StatusOK, map[string]any{
				"id":               42424242,
				"draft_body":       proseMirrorWith(marker),
				"draft_updated_at": "2026-07-28T14:00:00.000Z",
				"is_published":     false,
				"post_date":        nil,
				"email_sent_at":    nil,
				"postSchedules": []any{
					map[string]any{"trigger_at": "2026-07-29T14:00:00.000Z"},
				},
				"draftBylines": []any{
					map[string]any{"id": 10101, "is_guest": false},
				},
			})
		case request.Method == http.MethodPut:
			putCount.Add(1)
			t.Errorf("scheduled post received mutation: %s", request.URL.Path)
			writeJSON(t, response, http.StatusOK, loadUpdateDraftResponse(t))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	client := mustClient(t, server, "connect.sid=synthetic-session")
	_, err := client.UpdateDraft(
		context.Background(),
		postID,
		"Synthetic update title",
		proseMirrorWith(marker),
		marker,
	)
	if err == nil || !strings.Contains(err.Error(), "scheduled") {
		t.Fatalf("UpdateDraft() error = %v, want scheduled-post refusal", err)
	}
	if putCount.Load() != 0 {
		t.Fatalf("PUT count = %d, want 0", putCount.Load())
	}
}

func TestUpdateDraftRefusesPublishedPostBeforeMutation(t *testing.T) {
	t.Parallel()

	const (
		postID = "42424242"
		marker = "gtme-issue:11111111-2222-4333-8444-555555555555"
	)
	var putCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == "/api/v1/drafts/"+postID:
			if request.URL.RawQuery != "" {
				t.Errorf("draft detail query = %q, want empty", request.URL.RawQuery)
			}
			writeJSON(t, response, http.StatusOK, map[string]any{
				"id":               42424242,
				"draft_body":       proseMirrorWith(marker),
				"draft_updated_at": "2026-07-28T14:00:00.000Z",
				"is_published":     true,
				"post_date":        "2026-07-28T13:00:00.000Z",
				"email_sent_at":    nil,
				"postSchedules":    []any{},
				"draftBylines": []any{
					map[string]any{"id": 10101, "is_guest": false},
				},
			})
		case request.Method == http.MethodPut:
			putCount.Add(1)
			t.Errorf("published post received mutation: %s", request.URL.Path)
			writeJSON(t, response, http.StatusOK, loadUpdateDraftResponse(t))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	client := mustClient(t, server, "connect.sid=synthetic-session")
	_, err := client.UpdateDraft(
		context.Background(),
		postID,
		"Synthetic update title",
		proseMirrorWith(marker),
		marker,
	)
	if err == nil || !strings.Contains(err.Error(), "published") {
		t.Fatalf("UpdateDraft() error = %v, want published-post refusal", err)
	}
	if putCount.Load() != 0 {
		t.Fatalf("PUT count = %d, want 0", putCount.Load())
	}
}

func TestUpdateDraftRequiresMarkerToRoundTripExactlyOnce(t *testing.T) {
	t.Parallel()

	const (
		postID = "42424242"
		marker = "gtme-issue:11111111-2222-4333-8444-555555555555"
	)
	for _, test := range []struct {
		name         string
		responseBody string
	}{
		{name: "missing", responseBody: proseMirrorWith("synthetic-other-marker")},
		{name: "duplicated", responseBody: proseMirrorWith(marker + " " + marker)},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := updateDraftServer(t, postID, marker, func(response map[string]any) {
				response["draft_body"] = test.responseBody
			})
			defer server.Close()

			client := mustClient(t, server, "connect.sid=synthetic-session")
			_, err := client.UpdateDraft(
				context.Background(),
				postID,
				"Synthetic update title",
				proseMirrorWith(marker),
				marker,
			)
			if err == nil {
				t.Fatal("UpdateDraft() error = nil, want marker round-trip error")
			}
		})
	}
}

func TestUpdateDraftRejectsResponseForDifferentPost(t *testing.T) {
	t.Parallel()

	const (
		postID = "42424242"
		marker = "gtme-issue:11111111-2222-4333-8444-555555555555"
	)
	numericPostID := mustNumericPostID(t, postID)
	server := updateDraftServer(t, postID, marker, func(response map[string]any) {
		response["id"] = numericPostID + 1
	})
	defer server.Close()

	client := mustClient(t, server, "connect.sid=synthetic-session")
	_, err := client.UpdateDraft(
		context.Background(),
		postID,
		"Synthetic update title",
		proseMirrorWith(marker),
		marker,
	)
	if err == nil {
		t.Fatal("UpdateDraft() error = nil, want response-id mismatch error")
	}
}

func TestUpdateDraftRejectsNonDraftResponse(t *testing.T) {
	t.Parallel()

	const (
		postID = "42424242"
		marker = "gtme-issue:11111111-2222-4333-8444-555555555555"
	)
	server := updateDraftServer(t, postID, marker, func(response map[string]any) {
		response["is_published"] = true
		response["post_date"] = "2026-07-28T14:02:03.000Z"
	})
	defer server.Close()

	client := mustClient(t, server, "connect.sid=synthetic-session")
	_, err := client.UpdateDraft(
		context.Background(),
		postID,
		"Synthetic update title",
		proseMirrorWith(marker),
		marker,
	)
	if err == nil ||
		(!strings.Contains(err.Error(), "draft") &&
			!strings.Contains(err.Error(), "published")) {
		t.Fatalf("UpdateDraft() error = %v, want non-draft response error", err)
	}
}

func TestCreateDraftUsesCookieProfileBylineAndStringifiedBody(t *testing.T) {
	t.Parallel()

	const (
		cookie = "connect.sid=secret-session-value"
		marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
		body   = `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"` + marker + `"}]}]}`
	)

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if request.Header.Get("Cookie") != cookie {
			t.Errorf("Cookie header = %q, want supplied cookie", request.Header.Get("Cookie"))
		}
		switch request.URL.Path {
		case "/api/v1/user/profile/self":
			writeJSON(t, response, http.StatusOK, map[string]any{"id": 215222512})
		case "/api/v1/drafts":
			if request.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", request.Method)
			}
			if request.Header.Get("Referer") != server.URL+"/publish/home" {
				t.Errorf("Referer = %q", request.Header.Get("Referer"))
			}
			var payload map[string]any
			decodeJSON(t, request.Body, &payload)
			if payload["draft_title"] != "GTM jobs this week" {
				t.Errorf("draft_title = %#v", payload["draft_title"])
			}
			if payload["draft_body"] != body {
				t.Errorf("draft_body = %#v, want stringified document", payload["draft_body"])
			}
			bylines, ok := payload["draft_bylines"].([]any)
			if !ok || len(bylines) != 1 {
				t.Fatalf("draft_bylines = %#v", payload["draft_bylines"])
			}
			byline, ok := bylines[0].(map[string]any)
			if !ok || byline["id"] != float64(215222512) || byline["is_guest"] != false {
				t.Fatalf("draft_bylines[0] = %#v", bylines[0])
			}
			if payload["audience"] != "everyone" || payload["type"] != "newsletter" {
				t.Errorf("draft safety fields = audience:%#v type:%#v", payload["audience"], payload["type"])
			}
			writeJSON(t, response, http.StatusOK, map[string]any{
				"id":         208706412,
				"draft_body": body,
			})
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	client, err := substack.NewClient(server.URL, server.URL, cookie, server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	result, err := client.CreateDraft(
		context.Background(),
		"GTM jobs this week",
		body,
		marker,
	)
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}

	if result.DraftID != "208706412" {
		t.Errorf("DraftID = %q", result.DraftID)
	}
	if result.DraftURL != server.URL+"/publish/post/208706412" {
		t.Errorf("DraftURL = %q", result.DraftURL)
	}
	if result.Status != "draft" || result.CorrelationMarker != marker {
		t.Errorf("result = %#v", result)
	}
}

func TestFindByMarkerReturnsDraftFromManagementFeed(t *testing.T) {
	t.Parallel()

	const marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
	server := newRouteServer(t, map[string]routeResponse{
		"/api/v1/post_management/drafts": {
			status: http.StatusOK,
			body: map[string]any{
				"posts":    []any{map[string]any{"id": 208706412}},
				"total":    1,
				"limit":    100,
				"offset":   0,
				"isCapped": false,
			},
		},
		"/api/v1/drafts/208706412": {
			status: http.StatusOK,
			body: map[string]any{
				"id":               208706412,
				"draft_body":       proseMirrorWith(marker),
				"draft_updated_at": "2026-07-31T14:00:00.000Z",
				"postSchedules":    []any{},
			},
		},
		"/api/v1/post_management/scheduled": emptyFeed(),
		"/api/v1/post_management/published": emptyFeed(),
	})
	defer server.Close()

	client := mustClient(t, server, "connect.sid=session")
	result, err := client.FindByMarker(context.Background(), marker)
	if err != nil {
		t.Fatalf("FindByMarker() error = %v", err)
	}
	if !result.Found || result.Post == nil {
		t.Fatalf("result = %#v, want found post", result)
	}
	assertPost(t, *result.Post, "208706412", "draft", marker, nil, nil)
}

func TestFindByMarkerReturnsNotFoundWithoutPost(t *testing.T) {
	t.Parallel()

	server := newRouteServer(t, map[string]routeResponse{
		"/api/v1/post_management/drafts":    emptyFeed(),
		"/api/v1/post_management/scheduled": emptyFeed(),
		"/api/v1/post_management/published": emptyFeed(),
	})
	defer server.Close()

	client := mustClient(t, server, "connect.sid=session")
	result, err := client.FindByMarker(
		context.Background(),
		"gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d",
	)
	if err != nil {
		t.Fatalf("FindByMarker() error = %v", err)
	}
	if result.Found || result.Post != nil {
		t.Fatalf("result = %#v, want authoritative not found", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), `"post"`) {
		t.Fatalf("not-found JSON contains post: %s", encoded)
	}
}

func TestFindByMarkerPreservesScheduledFeedTimestamp(t *testing.T) {
	t.Parallel()

	const (
		marker      = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
		scheduledAt = "2026-07-28T16:00:00Z"
	)
	server := newRouteServer(t, map[string]routeResponse{
		"/api/v1/post_management/drafts": emptyFeed(),
		"/api/v1/post_management/scheduled": {
			status: http.StatusOK,
			body: map[string]any{
				"posts": []any{
					map[string]any{
						"id":         208706412,
						"trigger_at": scheduledAt,
					},
				},
				"total":    1,
				"limit":    100,
				"offset":   0,
				"isCapped": false,
			},
		},
		"/api/v1/drafts/208706412": {
			status: http.StatusOK,
			body: map[string]any{
				"id":               208706412,
				"draft_body":       proseMirrorWith(marker),
				"draft_updated_at": "2026-07-31T14:00:00.000Z",
			},
		},
		"/api/v1/post_management/published": emptyFeed(),
	})
	defer server.Close()

	client := mustClient(t, server, "connect.sid=session")
	result, err := client.FindByMarker(context.Background(), marker)
	if err != nil {
		t.Fatalf("FindByMarker() error = %v", err)
	}
	if !result.Found || result.Post == nil {
		t.Fatalf("result = %#v, want found post", result)
	}
	assertPost(t, *result.Post, "208706412", "scheduled", marker, stringPointer(scheduledAt), nil)
}

func TestFindByMarkerRejectsScheduledFeedRowWithoutTriggerAt(t *testing.T) {
	t.Parallel()

	server := newRouteServer(t, map[string]routeResponse{
		"/api/v1/post_management/drafts": emptyFeed(),
		"/api/v1/post_management/scheduled": {
			status: http.StatusOK,
			body: map[string]any{
				"posts":    []any{map[string]any{"id": 208706412}},
				"total":    1,
				"limit":    100,
				"offset":   0,
				"isCapped": false,
			},
		},
	})
	defer server.Close()

	client := mustClient(t, server, "connect.sid=session")
	result, err := client.FindByMarker(
		context.Background(),
		"gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d",
	)
	if err == nil || !strings.Contains(err.Error(), "scheduled feed row") {
		t.Fatalf("result = %#v, error = %v; want lifecycle-evidence error", result, err)
	}
}

func TestFindByMarkerRejectsPublishedFeedRowWithoutPostDate(t *testing.T) {
	t.Parallel()

	server := newRouteServer(t, map[string]routeResponse{
		"/api/v1/post_management/drafts":    emptyFeed(),
		"/api/v1/post_management/scheduled": emptyFeed(),
		"/api/v1/post_management/published": {
			status: http.StatusOK,
			body: map[string]any{
				"posts":    []any{map[string]any{"id": 208706412}},
				"total":    1,
				"limit":    100,
				"offset":   0,
				"isCapped": false,
			},
		},
	})
	defer server.Close()

	client := mustClient(t, server, "connect.sid=session")
	result, err := client.FindByMarker(
		context.Background(),
		"gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d",
	)
	if err == nil || !strings.Contains(err.Error(), "published feed row") {
		t.Fatalf("result = %#v, error = %v; want lifecycle-evidence error", result, err)
	}
}

func TestFindByMarkerReturnsPublishedPostFromGlobalEnvelope(t *testing.T) {
	t.Parallel()

	const (
		marker      = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
		publishedAt = "2026-07-28T16:05:00Z"
	)
	publicationServer := newRouteServer(t, map[string]routeResponse{
		"/api/v1/post_management/drafts":    emptyFeed(),
		"/api/v1/post_management/scheduled": emptyFeed(),
		"/api/v1/post_management/published": {
			status: http.StatusOK,
			body: map[string]any{
				"posts": []any{map[string]any{
					"id":        208706412,
					"post_date": publishedAt,
				}},
				"total":    1,
				"limit":    100,
				"offset":   0,
				"isCapped": false,
			},
		},
	})
	defer publicationServer.Close()
	accountServer := newRouteServer(t, map[string]routeResponse{
		"/api/v1/posts/by-id/208706412": {
			status: http.StatusOK,
			body: map[string]any{
				"post": map[string]any{
					"id":        208706412,
					"body_html": "<p>" + marker + "</p>",
					"post_date": publishedAt,
					"canonical_url": strings.Replace(
						publicationServer.URL,
						"http://",
						"https://",
						1,
					) + "/p/test-post",
				},
				"publication": map[string]any{
					"hostname": strings.TrimPrefix(publicationServer.URL, "http://"),
				},
			},
		},
	})
	defer accountServer.Close()

	client, err := substack.NewClient(
		publicationServer.URL,
		accountServer.URL,
		"connect.sid=session",
		publicationServer.Client(),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	result, err := client.FindByMarker(context.Background(), marker)
	if err != nil {
		t.Fatalf("FindByMarker() error = %v", err)
	}
	if !result.Found || result.Post == nil {
		t.Fatalf("result = %#v, want published post", result)
	}
	assertPost(t, *result.Post, "208706412", "published", marker, nil, stringPointer(publishedAt))
	if result.Post.PostURL != strings.Replace(
		publicationServer.URL,
		"http://",
		"https://",
		1,
	)+"/p/test-post" {
		t.Fatalf("PostURL = %q, want canonical reader URL", result.Post.PostURL)
	}
}

func TestFindByMarkerPrefersScheduledEvidenceWhenIDOverlapsDraftFeed(t *testing.T) {
	t.Parallel()

	const (
		marker      = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
		scheduledAt = "2026-07-28T16:00:00Z"
	)
	server := newRouteServer(t, map[string]routeResponse{
		"/api/v1/post_management/drafts": {
			status: http.StatusOK,
			body: map[string]any{
				"posts":    []any{map[string]any{"id": 208706412}},
				"total":    1,
				"limit":    100,
				"offset":   0,
				"isCapped": false,
			},
		},
		"/api/v1/post_management/scheduled": {
			status: http.StatusOK,
			body: map[string]any{
				"posts": []any{
					map[string]any{
						"id":         208706412,
						"trigger_at": scheduledAt,
					},
				},
				"total":    1,
				"limit":    100,
				"offset":   0,
				"isCapped": false,
			},
		},
		"/api/v1/post_management/published": emptyFeed(),
		"/api/v1/drafts/208706412": {
			status: http.StatusOK,
			body: map[string]any{
				"id":               208706412,
				"draft_body":       proseMirrorWith(marker),
				"draft_updated_at": "2026-07-31T14:00:00.000Z",
			},
		},
	})
	defer server.Close()

	client := mustClient(t, server, "connect.sid=session")
	result, err := client.FindByMarker(context.Background(), marker)
	if err != nil {
		t.Fatalf("FindByMarker() error = %v", err)
	}
	if !result.Found || result.Post == nil {
		t.Fatalf("result = %#v, want found post", result)
	}
	assertPost(t, *result.Post, "208706412", "scheduled", marker, stringPointer(scheduledAt), nil)
}

func TestFindByMarkerRejectsMultipleDistinctMatches(t *testing.T) {
	t.Parallel()

	const marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
	server := newRouteServer(t, map[string]routeResponse{
		"/api/v1/post_management/drafts": {
			status: http.StatusOK,
			body: map[string]any{
				"posts": []any{
					map[string]any{"id": 101},
					map[string]any{"id": 202},
				},
				"total":    2,
				"limit":    100,
				"offset":   0,
				"isCapped": false,
			},
		},
		"/api/v1/drafts/101": {
			status: http.StatusOK,
			body: map[string]any{
				"id":               101,
				"draft_body":       proseMirrorWith(marker),
				"draft_updated_at": "2026-07-31T14:00:00.000Z",
			},
		},
		"/api/v1/drafts/202": {
			status: http.StatusOK,
			body: map[string]any{
				"id":               202,
				"draft_body":       proseMirrorWith(marker),
				"draft_updated_at": "2026-07-31T14:00:00.000Z",
			},
		},
		"/api/v1/post_management/scheduled": emptyFeed(),
		"/api/v1/post_management/published": emptyFeed(),
	})
	defer server.Close()

	client := mustClient(t, server, "connect.sid=session")
	_, err := client.FindByMarker(context.Background(), marker)
	if err == nil || !strings.Contains(err.Error(), "multiple posts") {
		t.Fatalf("error = %v, want ambiguity error", err)
	}
}

func TestFindByMarkerRejectsCappedFeedInsteadOfReturningFalseNotFound(t *testing.T) {
	t.Parallel()

	server := newRouteServer(t, map[string]routeResponse{
		"/api/v1/post_management/drafts": {
			status: http.StatusOK,
			body: map[string]any{
				"posts":    []any{},
				"total":    1000,
				"limit":    100,
				"offset":   0,
				"isCapped": true,
			},
		},
	})
	defer server.Close()

	client := mustClient(t, server, "connect.sid=session")
	result, err := client.FindByMarker(
		context.Background(),
		"gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d",
	)
	if err == nil || !strings.Contains(err.Error(), "capped") {
		t.Fatalf("result = %#v, error = %v; want capped-feed error", result, err)
	}
}

func TestFindByMarkerRejectsFeedWithMissingPaginationContract(t *testing.T) {
	t.Parallel()

	server := newRouteServer(t, map[string]routeResponse{
		"/api/v1/post_management/drafts": {
			status: http.StatusOK,
			body:   map[string]any{},
		},
	})
	defer server.Close()

	client := mustClient(t, server, "connect.sid=session")
	result, err := client.FindByMarker(
		context.Background(),
		"gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d",
	)
	if err == nil || !strings.Contains(err.Error(), "missing required fields") {
		t.Fatalf(
			"result = %#v, error = %v; want missing-pagination error",
			result,
			err,
		)
	}
}

func TestFindByMarkerAdvancesByServerReturnedPageLimit(t *testing.T) {
	t.Parallel()

	const marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/api/v1/post_management/drafts":
			if request.URL.Query().Get("limit") != "10" {
				t.Fatalf(
					"draft limit = %q, want browser-observed maximum 10",
					request.URL.Query().Get("limit"),
				)
			}
			offset := request.URL.Query().Get("offset")
			switch offset {
			case "0":
				writeJSON(t, response, http.StatusOK, map[string]any{
					"posts":    []any{map[string]any{"id": 208706411}},
					"total":    2,
					"limit":    1,
					"offset":   0,
					"isCapped": false,
				})
			case "1":
				writeJSON(t, response, http.StatusOK, map[string]any{
					"posts":    []any{map[string]any{"id": 208706412}},
					"total":    2,
					"limit":    1,
					"offset":   1,
					"isCapped": false,
				})
			default:
				t.Fatalf("unexpected draft offset %q", offset)
			}
		case "/api/v1/drafts/208706411":
			writeJSON(t, response, http.StatusOK, map[string]any{
				"id":               208706411,
				"draft_updated_at": "2026-07-31T14:00:00.000Z",
				"draft_body": proseMirrorWith(
					"gtme-issue:11111111-1111-4111-8111-111111111111",
				),
			})
		case "/api/v1/drafts/208706412":
			writeJSON(t, response, http.StatusOK, map[string]any{
				"id":               208706412,
				"draft_body":       proseMirrorWith(marker),
				"draft_updated_at": "2026-07-31T14:00:00.000Z",
			})
		case "/api/v1/post_management/scheduled",
			"/api/v1/post_management/published":
			writeJSON(t, response, http.StatusOK, emptyFeed().body)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	client := mustClient(t, server, "connect.sid=session")
	result, err := client.FindByMarker(context.Background(), marker)
	if err != nil {
		t.Fatalf("FindByMarker() error = %v", err)
	}
	if !result.Found || result.Post == nil || result.Post.PostID != "208706412" {
		t.Fatalf("result = %#v, want second-page match", result)
	}
}

func TestFindByMarkerRejectsCandidateDetailIDMismatch(t *testing.T) {
	t.Parallel()

	const marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
	server := newRouteServer(t, map[string]routeResponse{
		"/api/v1/post_management/drafts": {
			status: http.StatusOK,
			body: map[string]any{
				"posts":    []any{map[string]any{"id": 208706412}},
				"total":    1,
				"limit":    100,
				"offset":   0,
				"isCapped": false,
			},
		},
		"/api/v1/drafts/208706412": {
			status: http.StatusOK,
			body: map[string]any{
				"id":         999999999,
				"draft_body": proseMirrorWith(marker),
			},
		},
		"/api/v1/post_management/scheduled": emptyFeed(),
		"/api/v1/post_management/published": emptyFeed(),
	})
	defer server.Close()

	client := mustClient(t, server, "connect.sid=session")
	result, err := client.FindByMarker(context.Background(), marker)
	if err == nil || !strings.Contains(err.Error(), "does not match candidate id") {
		t.Fatalf("result = %#v, error = %v; want id-mismatch error", result, err)
	}
}

func TestFindByMarkerRejectsCandidateDetailWithoutBody(t *testing.T) {
	t.Parallel()

	server := newRouteServer(t, map[string]routeResponse{
		"/api/v1/post_management/drafts": {
			status: http.StatusOK,
			body: map[string]any{
				"posts":    []any{map[string]any{"id": 208706412}},
				"total":    1,
				"limit":    100,
				"offset":   0,
				"isCapped": false,
			},
		},
		"/api/v1/drafts/208706412": {
			status: http.StatusOK,
			body:   map[string]any{"id": 208706412},
		},
		"/api/v1/post_management/scheduled": emptyFeed(),
		"/api/v1/post_management/published": emptyFeed(),
	})
	defer server.Close()

	client := mustClient(t, server, "connect.sid=session")
	result, err := client.FindByMarker(
		context.Background(),
		"gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d",
	)
	if err == nil || !strings.Contains(err.Error(), "missing body") {
		t.Fatalf("result = %#v, error = %v; want missing-body error", result, err)
	}
}

func TestGetPostNormalizesScheduledState(t *testing.T) {
	t.Parallel()

	const (
		marker      = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
		scheduledAt = "2026-07-28T16:00:00Z"
	)
	server := newRouteServer(t, map[string]routeResponse{
		"/api/v1/drafts/208706412": {
			status: http.StatusOK,
			body: map[string]any{
				"id":               208706412,
				"draft_body":       proseMirrorWith(marker),
				"draft_updated_at": "2026-07-31T14:00:00.000Z",
				"postSchedules": []any{
					map[string]any{"trigger_at": scheduledAt},
				},
			},
		},
	})
	defer server.Close()

	client := mustClient(t, server, "connect.sid=session")
	result, err := client.GetPost(context.Background(), "208706412")
	if err != nil {
		t.Fatalf("GetPost() error = %v", err)
	}
	if !result.Found || result.Post == nil {
		t.Fatalf("result = %#v, want found", result)
	}
	assertPost(t, *result.Post, "208706412", "scheduled", marker, stringPointer(scheduledAt), nil)
}

func TestGetPostReturnsDraftUpdatedAtForDraft(t *testing.T) {
	t.Parallel()

	const (
		postID  = "208706412"
		marker  = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
		updated = "2026-07-31T14:00:00.000Z"
	)
	var stage atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == "/api/v1/drafts/"+postID:
			if !stage.CompareAndSwap(0, 1) {
				t.Fatalf("draft detail request at stage %d", stage.Load())
			}
			writeJSON(t, response, http.StatusOK, map[string]any{
				"id":               208706412,
				"draft_body":       proseMirrorWith(marker),
				"draft_updated_at": updated,
				"postSchedules":    []any{},
			})
		case request.Method == http.MethodGet &&
			request.URL.Path == "/api/v1/post_management/scheduled":
			if !stage.CompareAndSwap(1, 2) {
				t.Fatalf("scheduled feed request at stage %d", stage.Load())
			}
			writeJSON(t, response, http.StatusOK, emptyScheduledUpdateFeed())
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	client := mustClient(t, server, "connect.sid=session")
	result, err := client.GetPost(context.Background(), postID)
	if err != nil {
		t.Fatalf("GetPost() error = %v", err)
	}
	if !result.Found || result.Post == nil {
		t.Fatalf("GetPost() result = %#v", result)
	}
	if result.Post.DraftUpdatedAt == nil || *result.Post.DraftUpdatedAt != updated {
		t.Fatalf("GetPost() draft_updated_at = %#v", result.Post.DraftUpdatedAt)
	}
}

func TestGetPostUsesScheduledFeedWhenDraftDetailOmitsSchedule(t *testing.T) {
	t.Parallel()

	const (
		marker      = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
		scheduledAt = "2026-07-28T16:00:00Z"
	)
	server := newRouteServer(t, map[string]routeResponse{
		"/api/v1/drafts/208706412": {
			status: http.StatusOK,
			body: map[string]any{
				"id":               208706412,
				"draft_body":       proseMirrorWith(marker),
				"draft_updated_at": "2026-07-31T14:00:00.000Z",
			},
		},
		"/api/v1/post_management/scheduled": {
			status: http.StatusOK,
			body: map[string]any{
				"posts": []any{
					map[string]any{
						"id":         208706412,
						"trigger_at": scheduledAt,
					},
				},
				"total":    1,
				"limit":    100,
				"offset":   0,
				"isCapped": false,
			},
		},
	})
	defer server.Close()

	client := mustClient(t, server, "connect.sid=session")
	result, err := client.GetPost(context.Background(), "208706412")
	if err != nil {
		t.Fatalf("GetPost() error = %v", err)
	}
	if !result.Found || result.Post == nil {
		t.Fatalf("result = %#v, want found", result)
	}
	assertPost(t, *result.Post, "208706412", "scheduled", marker, stringPointer(scheduledAt), nil)
}

func TestGetPostRejectsMatchingScheduledFeedRowWithoutTriggerAt(t *testing.T) {
	t.Parallel()

	const marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
	server := newRouteServer(t, map[string]routeResponse{
		"/api/v1/drafts/208706412": {
			status: http.StatusOK,
			body: map[string]any{
				"id":               208706412,
				"draft_body":       proseMirrorWith(marker),
				"draft_updated_at": "2026-07-31T14:00:00.000Z",
			},
		},
		"/api/v1/post_management/scheduled": {
			status: http.StatusOK,
			body: map[string]any{
				"posts":    []any{map[string]any{"id": 208706412}},
				"total":    1,
				"limit":    100,
				"offset":   0,
				"isCapped": false,
			},
		},
	})
	defer server.Close()

	client := mustClient(t, server, "connect.sid=session")
	result, err := client.GetPost(context.Background(), "208706412")
	if err == nil || !strings.Contains(err.Error(), "scheduled feed row") {
		t.Fatalf("result = %#v, error = %v; want lifecycle-evidence error", result, err)
	}
}

func TestGetPostNormalizesPublishedStateByID(t *testing.T) {
	t.Parallel()

	const (
		marker      = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
		publishedAt = "2026-07-28T16:05:00Z"
	)
	publicationServer := newRouteServer(t, map[string]routeResponse{
		"/api/v1/drafts/208706412": {
			status: http.StatusNotFound,
			body:   map[string]any{"error": "not found"},
		},
	})
	defer publicationServer.Close()
	accountServer := newRouteServer(t, map[string]routeResponse{
		"/api/v1/posts/by-id/208706412": {
			status: http.StatusOK,
			body: map[string]any{
				"post": map[string]any{
					"id":        208706412,
					"body_html": "<p>" + marker + "</p>",
					"post_date": publishedAt,
					"canonical_url": strings.Replace(
						publicationServer.URL,
						"http://",
						"https://",
						1,
					) + "/p/test-post",
				},
				"publication": map[string]any{
					"hostname": strings.TrimPrefix(publicationServer.URL, "http://"),
				},
			},
		},
	})
	defer accountServer.Close()

	client, err := substack.NewClient(
		publicationServer.URL,
		accountServer.URL,
		"connect.sid=session",
		publicationServer.Client(),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	result, err := client.GetPost(context.Background(), "208706412")
	if err != nil {
		t.Fatalf("GetPost() error = %v", err)
	}
	if !result.Found || result.Post == nil {
		t.Fatalf("result = %#v, want found", result)
	}
	assertPost(t, *result.Post, "208706412", "published", marker, nil, stringPointer(publishedAt))
	if result.Post.PostURL != strings.Replace(
		publicationServer.URL,
		"http://",
		"https://",
		1,
	)+"/p/test-post" {
		t.Fatalf("PostURL = %q, want canonical reader URL", result.Post.PostURL)
	}
}

func TestGetPostBuildsPublishedReaderURLFromSlug(t *testing.T) {
	t.Parallel()

	const marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
	const publishedAt = "2026-07-28T16:05:00Z"
	publicationServer := newRouteServer(t, map[string]routeResponse{
		"/api/v1/drafts/208706412": {
			status: http.StatusNotFound,
			body:   map[string]any{"error": "not found"},
		},
	})
	defer publicationServer.Close()
	accountServer := newRouteServer(t, map[string]routeResponse{
		"/api/v1/posts/by-id/208706412": {
			status: http.StatusOK,
			body: map[string]any{
				"post": map[string]any{
					"id":        208706412,
					"body_html": "<p>" + marker + "</p>",
					"post_date": publishedAt,
					"slug":      "test-post",
				},
				"publication": map[string]any{
					"hostname": strings.TrimPrefix(
						publicationServer.URL,
						"http://",
					),
				},
			},
		},
	})
	defer accountServer.Close()

	client, err := substack.NewClient(
		publicationServer.URL,
		accountServer.URL,
		"connect.sid=session",
		publicationServer.Client(),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	result, err := client.GetPost(context.Background(), "208706412")
	if err != nil {
		t.Fatalf("GetPost() error = %v", err)
	}
	if !result.Found || result.Post == nil {
		t.Fatalf("result = %#v, want found", result)
	}
	wantURL := strings.Replace(
		publicationServer.URL,
		"http://",
		"https://",
		1,
	) + "/p/test-post"
	if result.Post.PostURL != wantURL {
		t.Fatalf("PostURL = %q, want %q", result.Post.PostURL, wantURL)
	}
}

func TestGetPostRejectsUnsafePublishedSlug(t *testing.T) {
	t.Parallel()

	const marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
	publicationServer := newRouteServer(t, map[string]routeResponse{
		"/api/v1/drafts/208706412": {
			status: http.StatusNotFound,
			body:   map[string]any{"error": "not found"},
		},
	})
	defer publicationServer.Close()
	accountServer := newRouteServer(t, map[string]routeResponse{
		"/api/v1/posts/by-id/208706412": {
			status: http.StatusOK,
			body: map[string]any{
				"post": map[string]any{
					"id":        208706412,
					"body_html": "<p>" + marker + "</p>",
					"post_date": "2026-07-28T16:05:00Z",
					"slug":      "nested/path",
				},
				"publication": map[string]any{
					"hostname": strings.TrimPrefix(
						publicationServer.URL,
						"http://",
					),
				},
			},
		},
	})
	defer accountServer.Close()

	client, err := substack.NewClient(
		publicationServer.URL,
		accountServer.URL,
		"connect.sid=session",
		publicationServer.Client(),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	result, err := client.GetPost(context.Background(), "208706412")
	if err == nil || !strings.Contains(err.Error(), "safe slug") {
		t.Fatalf("result = %#v, error = %v; want unsafe slug error", result, err)
	}
}

func TestGetPostRejectsInvalidPublishedCanonicalURL(t *testing.T) {
	t.Parallel()

	const marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
	tests := []struct {
		name         string
		canonicalURL func(string) any
		errorText    string
	}{
		{
			name:         "missing",
			canonicalURL: func(string) any { return nil },
			errorText:    "canonical_url",
		},
		{
			name: "unsafe",
			canonicalURL: func(string) any {
				return "javascript:alert(1)"
			},
			errorText: "canonical_url",
		},
		{
			name: "different host",
			canonicalURL: func(string) any {
				return "https://other.example/p/test-post"
			},
			errorText: "does not match",
		},
		{
			name: "insecure",
			canonicalURL: func(publicationURL string) any {
				return publicationURL + "/p/test-post"
			},
			errorText: "HTTPS",
		},
		{
			name: "management path",
			canonicalURL: func(publicationURL string) any {
				return strings.Replace(
					publicationURL,
					"http://",
					"https://",
					1,
				) + "/publish/post/208706412"
			},
			errorText: "reader URL",
		},
		{
			name: "empty reader slug",
			canonicalURL: func(publicationURL string) any {
				return strings.Replace(
					publicationURL,
					"http://",
					"https://",
					1,
				) + "/p/"
			},
			errorText: "reader URL",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			publicationServer := newRouteServer(t, map[string]routeResponse{
				"/api/v1/drafts/208706412": {
					status: http.StatusNotFound,
					body:   map[string]any{"error": "not found"},
				},
			})
			defer publicationServer.Close()
			post := map[string]any{
				"id":        208706412,
				"body_html": "<p>" + marker + "</p>",
				"post_date": "2026-07-28T16:05:00Z",
			}
			canonicalURL := test.canonicalURL(publicationServer.URL)
			if canonicalURL != nil {
				post["canonical_url"] = canonicalURL
			}
			accountServer := newRouteServer(t, map[string]routeResponse{
				"/api/v1/posts/by-id/208706412": {
					status: http.StatusOK,
					body: map[string]any{
						"post": post,
						"publication": map[string]any{
							"hostname": strings.TrimPrefix(publicationServer.URL, "http://"),
						},
					},
				},
			})
			defer accountServer.Close()

			client, err := substack.NewClient(
				publicationServer.URL,
				accountServer.URL,
				"connect.sid=session",
				publicationServer.Client(),
			)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			result, err := client.GetPost(context.Background(), "208706412")
			if err == nil || !strings.Contains(err.Error(), test.errorText) {
				t.Fatalf("result = %#v, error = %v; want %q", result, err, test.errorText)
			}
		})
	}
}

func TestGetPostRejectsPublishedPostFromDifferentPublication(t *testing.T) {
	t.Parallel()

	const marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
	publicationServer := newRouteServer(t, map[string]routeResponse{
		"/api/v1/drafts/208706412": {
			status: http.StatusNotFound,
			body:   map[string]any{"error": "not found"},
		},
	})
	defer publicationServer.Close()
	accountServer := newRouteServer(t, map[string]routeResponse{
		"/api/v1/posts/by-id/208706412": {
			status: http.StatusOK,
			body: map[string]any{
				"post": map[string]any{
					"id":        208706412,
					"body_html": "<p>" + marker + "</p>",
					"post_date": "2026-07-28T16:05:00Z",
				},
				"publication": map[string]any{
					"hostname": "other-publication.substack.com",
				},
			},
		},
	})
	defer accountServer.Close()

	client, err := substack.NewClient(
		publicationServer.URL,
		accountServer.URL,
		"connect.sid=session",
		publicationServer.Client(),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	result, err := client.GetPost(context.Background(), "208706412")
	if err == nil || !strings.Contains(err.Error(), "publication hostname") {
		t.Fatalf("result = %#v, error = %v; want publication mismatch", result, err)
	}
}

func TestGetPostReturnsAuthoritativeNotFoundOnlyAfterBoth404(t *testing.T) {
	t.Parallel()

	server := newRouteServer(t, map[string]routeResponse{
		"/api/v1/drafts/missing": {
			status: http.StatusNotFound,
			body:   map[string]any{"error": "not found"},
		},
		"/api/v1/posts/by-id/missing": {
			status: http.StatusNotFound,
			body:   map[string]any{"error": "not found"},
		},
	})
	defer server.Close()

	client := mustClient(t, server, "connect.sid=session")
	result, err := client.GetPost(context.Background(), "missing")
	if err != nil {
		t.Fatalf("GetPost() error = %v", err)
	}
	if result.Found || result.Post != nil {
		t.Fatalf("result = %#v, want not found", result)
	}
}

func TestGetPostDoesNotNormalizePermissionFailureAsMissingOrLeakCookie(t *testing.T) {
	t.Parallel()

	const cookie = "connect.sid=do-not-leak-this"
	server := newRouteServer(t, map[string]routeResponse{
		"/api/v1/drafts/208706412": {
			status: http.StatusNotFound,
			body:   map[string]any{"error": "not found"},
		},
		"/api/v1/posts/by-id/208706412": {
			status: http.StatusForbidden,
			body:   map[string]any{"error": cookie},
		},
	})
	defer server.Close()

	client := mustClient(t, server, cookie)
	result, err := client.GetPost(context.Background(), "208706412")
	if err == nil || result.Found {
		t.Fatalf("result = %#v, error = %v; want permission error", result, err)
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("error = %v, want status", err)
	}
	assertSecretAbsent(t, err, cookie)
}

func TestGetPostRejectsTrailingJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusOK)
		if _, err := io.WriteString(
			response,
			`{"id":208706412,"draft_body":"body"}{"unexpected":true}`,
		); err != nil {
			t.Fatalf("write response: %v", err)
		}
	}))
	defer server.Close()

	client := mustClient(t, server, "connect.sid=session")
	result, err := client.GetPost(context.Background(), "208706412")
	if err == nil || !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("result = %#v, error = %v; want trailing-JSON error", result, err)
	}
}

func updateDraftServer(
	t *testing.T,
	postID string,
	marker string,
	mutateResponse func(map[string]any),
) *httptest.Server {
	t.Helper()
	numericPostID := mustNumericPostID(t, postID)
	var stage atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == "/api/v1/drafts/"+postID:
			if request.URL.RawQuery != "" {
				t.Errorf("draft detail query = %q, want empty", request.URL.RawQuery)
			}
			switch {
			case stage.CompareAndSwap(0, 1):
				writeJSON(t, response, http.StatusOK, map[string]any{
					"id":               numericPostID,
					"draft_body":       proseMirrorWith(marker),
					"draft_updated_at": "2026-07-28T14:00:00.000Z",
					"postSchedules":    []any{},
				})
			case stage.CompareAndSwap(2, 3):
				writeJSON(t, response, http.StatusOK, map[string]any{
					"id":               numericPostID,
					"draft_body":       proseMirrorWith(marker),
					"draft_updated_at": "2026-07-28T14:00:00.000Z",
					"is_published":     false,
					"post_date":        nil,
					"email_sent_at":    nil,
					"postSchedules":    []any{},
					"draftBylines": []any{
						map[string]any{"id": 10101, "is_guest": false},
					},
				})
			default:
				t.Fatalf("draft detail request at stage %d", stage.Load())
			}
		case request.Method == http.MethodGet &&
			request.URL.Path == "/api/v1/post_management/scheduled":
			if request.URL.RawQuery != scheduledFeedQuery {
				t.Errorf(
					"scheduled feed query = %q, want %q",
					request.URL.RawQuery,
					scheduledFeedQuery,
				)
			}
			if !stage.CompareAndSwap(1, 2) {
				t.Fatalf("scheduled feed request at stage %d", stage.Load())
			}
			writeJSON(t, response, http.StatusOK, emptyScheduledUpdateFeed())
		case request.Method == http.MethodPut &&
			request.URL.Path == "/api/v1/drafts/"+postID:
			if request.URL.RawQuery != "" {
				t.Errorf("PUT query = %q, want empty", request.URL.RawQuery)
			}
			if !stage.CompareAndSwap(3, 4) {
				t.Errorf("PUT was not immediately preceded by the lifecycle recheck")
			}
			updateResponse := loadUpdateDraftResponse(t)
			updateResponse["id"] = numericPostID
			updateResponse["draft_body"] = proseMirrorWith(marker)
			mutateResponse(updateResponse)
			writeJSON(t, response, http.StatusOK, updateResponse)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	t.Cleanup(func() {
		if stage.Load() != 4 {
			t.Errorf("request stage = %d, want safe four-request sequence", stage.Load())
		}
	})
	return server
}

func updateDraftRefreshFailureServer(
	t *testing.T,
	postID string,
	marker string,
	mutateRefresh func(map[string]any),
) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	numericPostID := mustNumericPostID(t, postID)
	var stage atomic.Int32
	var putCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == "/api/v1/drafts/"+postID:
			switch {
			case stage.CompareAndSwap(0, 1):
				writeJSON(t, response, http.StatusOK, map[string]any{
					"id":               numericPostID,
					"draft_body":       proseMirrorWith(marker),
					"draft_updated_at": "2026-07-28T14:00:00.000Z",
					"postSchedules":    []any{},
				})
			case stage.CompareAndSwap(2, 3):
				refresh := map[string]any{
					"id":               numericPostID,
					"draft_body":       proseMirrorWith(marker),
					"draft_updated_at": "2026-07-28T14:00:00.000Z",
					"is_published":     false,
					"post_date":        nil,
					"email_sent_at":    nil,
					"postSchedules":    []any{},
					"draftBylines": []any{
						map[string]any{"id": 10101, "is_guest": false},
					},
				}
				mutateRefresh(refresh)
				writeJSON(t, response, http.StatusOK, refresh)
			default:
				t.Fatalf("draft detail request at stage %d", stage.Load())
			}
		case request.Method == http.MethodGet &&
			request.URL.Path == "/api/v1/post_management/scheduled":
			if !stage.CompareAndSwap(1, 2) {
				t.Fatalf("scheduled feed request at stage %d", stage.Load())
			}
			writeJSON(t, response, http.StatusOK, emptyScheduledUpdateFeed())
		case request.Method == http.MethodPut &&
			request.URL.Path == "/api/v1/drafts/"+postID:
			putCount.Add(1)
			writeJSON(t, response, http.StatusOK, loadUpdateDraftResponse(t))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	t.Cleanup(func() {
		if stage.Load() != 3 {
			t.Errorf("request stage = %d, want pre-mutation refresh", stage.Load())
		}
	})
	return server, &putCount
}

func loadUpdateDraftResponse(t *testing.T) map[string]any {
	t.Helper()
	contents, err := os.ReadFile("testdata/update-draft-response.json")
	if err != nil {
		t.Fatalf("read update draft fixture: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(contents, &response); err != nil {
		t.Fatalf("decode update draft fixture: %v", err)
	}
	return response
}

func assertUpdatedDraftReturned(
	t *testing.T,
	result substack.UpdatedDraft,
	postID string,
	draftURL string,
	marker string,
) {
	t.Helper()
	if result.PostID != postID ||
		result.DraftURL != draftURL ||
		result.Status != "draft" ||
		result.CorrelationMarker != marker {
		t.Errorf("UpdateDraft() result = %#v", result)
	}
}

func mustNumericPostID(t *testing.T, postID string) int64 {
	t.Helper()
	numericPostID, err := strconv.ParseInt(postID, 10, 64)
	if err != nil {
		t.Fatalf("synthetic post id %q is not numeric: %v", postID, err)
	}
	return numericPostID
}

const scheduledFeedQuery = "limit=10&offset=0&order_by=trigger_at&order_direction=asc"

func emptyScheduledUpdateFeed() map[string]any {
	return map[string]any{
		"posts":    []any{},
		"total":    0,
		"limit":    10,
		"offset":   0,
		"isCapped": false,
	}
}

func decodeJSON(t *testing.T, reader io.Reader, target any) {
	t.Helper()
	if err := json.NewDecoder(reader).Decode(target); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
}

func writeJSON(t *testing.T, response http.ResponseWriter, status int, value any) {
	t.Helper()
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(value); err != nil {
		t.Fatalf("encode JSON: %v", err)
	}
}

func assertSecretAbsent(t *testing.T, err error, secret string) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked secret: %v", err)
	}
}

type routeResponse struct {
	status int
	body   any
}

func newRouteServer(t *testing.T, routes map[string]routeResponse) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		route, ok := routes[request.URL.Path]
		if !ok {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		writeJSON(t, response, route.status, route.body)
	}))
}

func emptyFeed() routeResponse {
	return routeResponse{
		status: http.StatusOK,
		body: map[string]any{
			"posts":    []any{},
			"total":    0,
			"limit":    100,
			"offset":   0,
			"isCapped": false,
		},
	}
}

func mustClient(t *testing.T, server *httptest.Server, cookie string) *substack.Client {
	t.Helper()
	client, err := substack.NewClient(server.URL, server.URL, cookie, server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func proseMirrorWith(marker string) string {
	return `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"` +
		marker +
		`"}]}]}`
}

func stringPointer(value string) *string {
	return &value
}

func assertPost(
	t *testing.T,
	post substack.Post,
	id string,
	status string,
	marker string,
	scheduledAt *string,
	publishedAt *string,
) {
	t.Helper()
	if post.PostID != id || post.Status != status || post.CorrelationMarker != marker {
		t.Errorf("post = %#v", post)
	}
	if !equalStringPointers(post.ScheduledAt, scheduledAt) {
		t.Errorf("ScheduledAt = %#v, want %#v", post.ScheduledAt, scheduledAt)
	}
	if !equalStringPointers(post.PublishedAt, publishedAt) {
		t.Errorf("PublishedAt = %#v, want %#v", post.PublishedAt, publishedAt)
	}
}

func equalStringPointers(left *string, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
