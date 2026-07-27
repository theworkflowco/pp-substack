package substack_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theworkflowco/pp-substack/internal/substack"
)

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
				"id":            208706412,
				"draft_body":    proseMirrorWith(marker),
				"postSchedules": []any{},
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
				"id":         208706412,
				"draft_body": proseMirrorWith(marker),
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
				"id":         208706412,
				"draft_body": proseMirrorWith(marker),
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
			body:   map[string]any{"id": 101, "draft_body": proseMirrorWith(marker)},
		},
		"/api/v1/drafts/202": {
			status: http.StatusOK,
			body:   map[string]any{"id": 202, "draft_body": proseMirrorWith(marker)},
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
				"id": 208706411,
				"draft_body": proseMirrorWith(
					"gtme-issue:11111111-1111-4111-8111-111111111111",
				),
			})
		case "/api/v1/drafts/208706412":
			writeJSON(t, response, http.StatusOK, map[string]any{
				"id":         208706412,
				"draft_body": proseMirrorWith(marker),
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
				"id":         208706412,
				"draft_body": proseMirrorWith(marker),
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
				"id":         208706412,
				"draft_body": proseMirrorWith(marker),
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
				"id":         208706412,
				"draft_body": proseMirrorWith(marker),
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
					"id":            208706412,
					"body_html":     "<p>" + marker + "</p>",
					"post_date":     publishedAt,
					"canonical_url": "https://publication.example/p/test-post",
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
