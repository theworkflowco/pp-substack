package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/theworkflowco/pp-substack/internal/cli"
	"github.com/theworkflowco/pp-substack/internal/substack"
)

func TestVersionJSON(t *testing.T) {
	t.Parallel()

	output, err := execute(t, cli.Options{Version: "0.1.0"}, "version", "--json")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	assertJSONEqual(t, output, `{"version":"0.1.0"}`)
}

func TestRootExposesOnlySixLeafCommands(t *testing.T) {
	t.Parallel()

	root := cli.NewRoot(cli.Options{Version: "0.1.0"})
	root.SetArgs([]string{"--help"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	paths := make([]string, 0)
	var walk func(prefix string, commandNames []string)
	walk = func(prefix string, commandNames []string) {
		for _, name := range commandNames {
			if name == "help" {
				continue
			}
			command, _, err := root.Find(strings.Fields(strings.TrimSpace(prefix + " " + name)))
			if err != nil {
				t.Fatalf("find %q: %v", name, err)
			}
			children := command.Commands()
			childNames := make([]string, 0, len(children))
			for _, child := range children {
				childNames = append(childNames, child.Name())
			}
			if len(childNames) == 0 {
				paths = append(paths, strings.TrimSpace(prefix+" "+name))
				continue
			}
			walk(strings.TrimSpace(prefix+" "+name), childNames)
		}
	}
	topLevel := make([]string, 0)
	for _, command := range root.Commands() {
		topLevel = append(topLevel, command.Name())
	}
	walk("", topLevel)
	sort.Strings(paths)

	expected := []string{
		"drafts compare",
		"drafts create",
		"drafts find",
		"drafts update",
		"posts get",
		"version",
	}
	if strings.Join(paths, ",") != strings.Join(expected, ",") {
		t.Fatalf("leaf commands = %v, want %v", paths, expected)
	}
}

func TestDraftCompareConvertsMarkdownAndPrintsStableJSON(t *testing.T) {
	t.Parallel()

	const marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
	fake := &fakeService{
		compareResult: substack.DraftComparison{
			PostID:            "208706412",
			Status:            "draft",
			Matches:           true,
			TitleMatches:      true,
			BodyMatches:       true,
			DraftUpdatedAt:    "2026-07-31T14:00:00.000Z",
			CorrelationMarker: marker,
		},
	}
	options := authenticatedOptions(fake)
	options.ReadFile = func(string) ([]byte, error) {
		return []byte("# Updated GTM jobs\n\nIssue reference: " + marker + "\n"), nil
	}

	output, err := execute(
		t,
		options,
		"drafts",
		"compare",
		"--publication",
		"gtmengineersearch",
		"--post-id",
		"208706412",
		"--title",
		"Updated GTM jobs this week",
		"--markdown-file",
		"/private/tmp/updated-issue.md",
		"--correlation-marker",
		marker,
		"--json",
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if fake.compareCalls != 1 || fake.comparePostID != "208706412" ||
		fake.compareTitle != "Updated GTM jobs this week" ||
		fake.compareMarker != marker || !strings.Contains(fake.compareBody, marker) {
		t.Fatalf("CompareDraft() call = %#v", fake)
	}
	assertJSONEqual(t, output, `{
		"post_id":"208706412",
		"status":"draft",
		"matches":true,
		"title_matches":true,
		"body_matches":true,
		"draft_updated_at":"2026-07-31T14:00:00.000Z",
		"correlation_marker":"`+marker+`"
	}`)
}

func TestDraftUpdateRequiresAllFlags(t *testing.T) {
	t.Parallel()

	const marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
	flags := []struct {
		name  string
		value string
	}{
		{name: "publication", value: "gtmengineersearch"},
		{name: "post-id", value: "208706412"},
		{name: "title", value: "Updated GTM jobs this week"},
		{name: "markdown-file", value: "/private/tmp/issue.md"},
		{name: "correlation-marker", value: marker},
		{name: "json"},
	}
	for _, omitted := range flags {
		omitted := omitted
		t.Run(omitted.name, func(t *testing.T) {
			t.Parallel()

			args := []string{"drafts", "update"}
			for _, flag := range flags {
				if flag.name == omitted.name {
					continue
				}
				args = append(args, "--"+flag.name)
				if flag.value != "" {
					args = append(args, flag.value)
				}
			}
			_, err := execute(
				t,
				authenticatedOptions(&fakeService{}),
				args...,
			)
			if err == nil || !strings.Contains(err.Error(), "--"+omitted.name) {
				t.Fatalf("error = %v, want missing --%s error", err, omitted.name)
			}
			if cli.ExitCode(err) != 2 {
				t.Fatalf("ExitCode() = %d, want usage exit 2", cli.ExitCode(err))
			}
		})
	}
}

func TestDraftUpdateConvertsMarkdownAndCallsService(t *testing.T) {
	t.Parallel()

	const (
		cookie = "connect.sid=update-secret-cookie"
		marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
	)
	fake := &fakeService{
		updateResult: substack.UpdatedDraft{
			PostID:            "208706412",
			DraftURL:          "https://gtmengineersearch.substack.com/publish/post/208706412",
			Status:            "draft",
			CorrelationMarker: marker,
		},
	}
	options := cli.Options{
		Version: "0.1.0",
		LookupEnv: func(name string) (string, bool) {
			if name == "PP_SUBSTACK_SESSION_COOKIE" {
				return cookie, true
			}
			return "", false
		},
		ReadFile: func(path string) ([]byte, error) {
			if path != "/private/tmp/updated-issue.md" {
				t.Fatalf("ReadFile path = %q", path)
			}
			return []byte("# Updated GTM jobs\n\nIssue reference: " + marker + "\n"), nil
		},
		NewService: func(publication string, receivedCookie string) (cli.Service, error) {
			if publication != "gtmengineersearch" {
				t.Errorf("publication = %q", publication)
			}
			if receivedCookie != cookie {
				t.Errorf("cookie was not passed to service")
			}
			return fake, nil
		},
	}

	_, err := execute(
		t,
		options,
		"drafts",
		"update",
		"--publication",
		"gtmengineersearch",
		"--post-id",
		"208706412",
		"--title",
		"Updated GTM jobs this week",
		"--markdown-file",
		"/private/tmp/updated-issue.md",
		"--correlation-marker",
		marker,
		"--json",
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if fake.updateCalls != 1 {
		t.Fatalf("UpdateDraft() calls = %d, want 1", fake.updateCalls)
	}
	if fake.updatePostID != "208706412" {
		t.Errorf("UpdateDraft() post id = %q", fake.updatePostID)
	}
	if fake.updateTitle != "Updated GTM jobs this week" {
		t.Errorf("UpdateDraft() title = %q", fake.updateTitle)
	}
	if fake.updateMarker != marker {
		t.Errorf("UpdateDraft() marker = %q", fake.updateMarker)
	}
	if strings.Count(fake.updateBody, marker) != 1 {
		t.Errorf(
			"UpdateDraft() converted body marker count = %d",
			strings.Count(fake.updateBody, marker),
		)
	}
	if !strings.Contains(fake.updateBody, "Updated GTM jobs") {
		t.Errorf("UpdateDraft() body = %q", fake.updateBody)
	}
}

func TestDraftUpdatePrintsStableJSON(t *testing.T) {
	t.Parallel()

	const marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
	fake := &fakeService{
		updateResult: substack.UpdatedDraft{
			PostID:            "208706412",
			DraftURL:          "https://gtmengineersearch.substack.com/publish/post/208706412",
			Status:            "draft",
			CorrelationMarker: marker,
		},
	}
	options := authenticatedOptions(fake)
	options.ReadFile = func(string) ([]byte, error) {
		return []byte("Issue reference: " + marker + "\n"), nil
	}

	output, err := execute(
		t,
		options,
		"drafts",
		"update",
		"--publication",
		"gtmengineersearch",
		"--post-id",
		"208706412",
		"--title",
		"Updated GTM jobs this week",
		"--markdown-file",
		"/private/tmp/updated-issue.md",
		"--correlation-marker",
		marker,
		"--json",
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	const expected = `{"post_id":"208706412","draft_url":"https://gtmengineersearch.substack.com/publish/post/208706412","status":"draft","correlation_marker":"` + marker + `"}` + "\n"
	if output != expected {
		t.Fatalf("output = %q, want %q", output, expected)
	}
}

func TestDraftUpdateErrorEnvelopeIsStableAndSecretFree(t *testing.T) {
	t.Parallel()

	const (
		marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
		cookie = "connect.sid=never-print-this-session"
	)
	fake := &fakeService{
		updateError: &substack.UpdateError{
			Stage:              substack.UpdateStageMutationUnknown,
			Code:               "update_transport_failed",
			MutationDispatched: true,
			Cause:              errors.New("transport failed: " + cookie),
		},
	}
	options := authenticatedOptions(fake)
	options.LookupEnv = func(name string) (string, bool) {
		if name == "PP_SUBSTACK_SESSION_COOKIE" {
			return cookie, true
		}
		return "", false
	}
	options.ReadFile = func(string) ([]byte, error) {
		return []byte("Issue reference: " + marker + "\n"), nil
	}

	_, err := execute(
		t,
		options,
		"drafts",
		"update",
		"--publication",
		"gtmengineersearch",
		"--post-id",
		"208706412",
		"--title",
		"Updated GTM jobs this week",
		"--markdown-file",
		"/private/tmp/updated-issue.md",
		"--correlation-marker",
		marker,
		"--json",
	)
	if err == nil {
		t.Fatal("Execute() error = nil, want staged update error")
	}

	output, ok := cli.ErrorOutput(err)
	if !ok {
		t.Fatalf("ErrorOutput() ok = false for %T", err)
	}
	var envelope struct {
		SchemaVersion      string `json:"schema_version"`
		Stage              string `json:"stage"`
		Code               string `json:"code"`
		MutationDispatched bool   `json:"mutation_dispatched"`
		Message            string `json:"message"`
	}
	if decodeErr := json.Unmarshal(output, &envelope); decodeErr != nil {
		t.Fatalf("ErrorOutput() = %q: %v", output, decodeErr)
	}
	if envelope.SchemaVersion != "pp-substack-update-error-v1" ||
		envelope.Stage != "mutation_unknown" ||
		envelope.Code != "update_transport_failed" ||
		!envelope.MutationDispatched ||
		envelope.Message != "Substack draft update result is unknown" {
		t.Fatalf("ErrorOutput() envelope = %#v", envelope)
	}
	if strings.Contains(string(output), cookie) {
		t.Fatalf("ErrorOutput() leaked session cookie: %q", output)
	}
}

func TestDraftUpdateNeverAcceptsCookieFlag(t *testing.T) {
	t.Parallel()

	const cookie = "connect.sid=must-not-be-a-flag"
	output, err := execute(
		t,
		authenticatedOptions(&fakeService{}),
		"drafts",
		"update",
		"--publication",
		"gtmengineersearch",
		"--post-id",
		"208706412",
		"--title",
		"Updated GTM jobs this week",
		"--markdown-file",
		"/private/tmp/updated-issue.md",
		"--correlation-marker",
		"gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d",
		"--json",
		"--cookie",
		cookie,
	)
	if err == nil {
		t.Fatal("Execute() error = nil, want cookie flag rejection")
	}
	if strings.Contains(output, cookie) || strings.Contains(err.Error(), cookie) {
		t.Fatalf("cookie flag leaked value: output=%q error=%v", output, err)
	}
	if cli.ExitCode(err) != 2 {
		t.Fatalf("ExitCode() = %d, want usage exit 2", cli.ExitCode(err))
	}
}

func TestDraftUpdateRedactsMalformedFlagValues(t *testing.T) {
	t.Parallel()

	const secret = "connect.sid=malformed-flag-secret"
	output, err := execute(
		t,
		authenticatedOptions(&fakeService{}),
		"drafts",
		"update",
		"--json="+secret,
	)
	if err == nil {
		t.Fatal("Execute() error = nil, want malformed flag rejection")
	}
	if strings.Contains(output, secret) || strings.Contains(err.Error(), secret) {
		t.Fatalf("malformed flag leaked value: output=%q error=%v", output, err)
	}
	if cli.ExitCode(err) != 2 {
		t.Fatalf("ExitCode() = %d, want usage exit 2", cli.ExitCode(err))
	}
}

func TestDraftsCreateReadsMarkdownAndPassesCookieOnlyToService(t *testing.T) {
	t.Parallel()

	const (
		cookie = "connect.sid=secret-cookie"
		marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
	)
	fake := &fakeService{
		createResult: substack.Draft{
			DraftID:           "208706412",
			DraftURL:          "https://gtmengineersearch.substack.com/publish/post/208706412",
			Status:            "draft",
			CorrelationMarker: marker,
		},
	}
	options := cli.Options{
		Version: "0.1.0",
		LookupEnv: func(name string) (string, bool) {
			if name == "PP_SUBSTACK_SESSION_COOKIE" {
				return cookie, true
			}
			return "", false
		},
		ReadFile: func(path string) ([]byte, error) {
			if path != "/private/tmp/issue.md" {
				t.Fatalf("ReadFile path = %q", path)
			}
			return []byte("# GTM jobs\n\nIssue reference: " + marker + "\n"), nil
		},
		NewService: func(publication string, receivedCookie string) (cli.Service, error) {
			if publication != "gtmengineersearch" {
				t.Errorf("publication = %q", publication)
			}
			if receivedCookie != cookie {
				t.Errorf("cookie was not passed to service")
			}
			return fake, nil
		},
	}

	output, err := execute(
		t,
		options,
		"drafts",
		"create",
		"--publication",
		"gtmengineersearch",
		"--title",
		"GTM jobs this week",
		"--markdown-file",
		"/private/tmp/issue.md",
		"--correlation-marker",
		marker,
		"--json",
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(output, cookie) {
		t.Fatalf("stdout leaked cookie: %s", output)
	}
	assertJSONEqual(
		t,
		output,
		`{
			"draft_id":"208706412",
			"draft_url":"https://gtmengineersearch.substack.com/publish/post/208706412",
			"status":"draft",
			"correlation_marker":"`+marker+`"
		}`,
	)
	if fake.createTitle != "GTM jobs this week" {
		t.Errorf("create title = %q", fake.createTitle)
	}
	if strings.Count(fake.createBody, marker) != 1 {
		t.Errorf("converted body marker count = %d", strings.Count(fake.createBody, marker))
	}
}

func TestDraftsFindNotFoundOmitsPost(t *testing.T) {
	t.Parallel()

	fake := &fakeService{findResult: substack.Found{Found: false}}
	output, err := execute(
		t,
		authenticatedOptions(fake),
		"drafts",
		"find",
		"--publication",
		"gtmengineersearch",
		"--correlation-marker",
		"gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d",
		"--json",
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	assertJSONEqual(t, output, `{"found":false}`)
	if strings.Contains(output, `"post"`) {
		t.Fatalf("not-found output contains post: %s", output)
	}
}

func TestDraftsFindFoundEmitsNormalizedPost(t *testing.T) {
	t.Parallel()

	const marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
	const updated = "2026-07-31T14:00:00.000Z"
	fake := &fakeService{
		findResult: substack.Found{
			Found: true,
			Post: &substack.Post{
				PostID:            "208706412",
				PostURL:           "https://gtmengineersearch.substack.com/publish/post/208706412",
				Status:            "draft",
				DraftUpdatedAt:    stringPointer(updated),
				CorrelationMarker: marker,
			},
		},
	}
	output, err := execute(
		t,
		authenticatedOptions(fake),
		"drafts",
		"find",
		"--publication",
		"gtmengineersearch",
		"--correlation-marker",
		marker,
		"--json",
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	assertJSONEqual(
		t,
		output,
		`{"found":true,"post":{
			"post_id":"208706412",
			"post_url":"https://gtmengineersearch.substack.com/publish/post/208706412",
			"status":"draft",
			"scheduled_at":null,
			"published_at":null,
			"draft_updated_at":"`+updated+`",
			"correlation_marker":"`+marker+`"
		}}`,
	)
}

func TestPostsGetReturnsFoundEnvelope(t *testing.T) {
	t.Parallel()

	const marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
	const updated = "2026-07-31T14:00:00.000Z"
	fake := &fakeService{
		getResult: substack.Found{
			Found: true,
			Post: &substack.Post{
				PostID:            "208706412",
				PostURL:           "https://gtmengineersearch.substack.com/publish/post/208706412",
				Status:            "draft",
				ScheduledAt:       nil,
				PublishedAt:       nil,
				DraftUpdatedAt:    stringPointer(updated),
				CorrelationMarker: marker,
			},
		},
	}
	output, err := execute(
		t,
		authenticatedOptions(fake),
		"posts",
		"get",
		"--publication",
		"gtmengineersearch",
		"--post-id",
		"208706412",
		"--json",
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	assertJSONEqual(
		t,
		output,
		`{
			"found":true,
			"post":{
				"post_id":"208706412",
				"post_url":"https://gtmengineersearch.substack.com/publish/post/208706412",
				"status":"draft",
				"scheduled_at":null,
				"published_at":null,
				"draft_updated_at":"`+updated+`",
				"correlation_marker":"`+marker+`"
			}
		}`,
	)
}

func TestPostsGetEmitsScheduledPublishedAndDeletedStates(t *testing.T) {
	t.Parallel()

	const (
		marker      = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
		scheduledAt = "2026-07-28T16:00:00Z"
		publishedAt = "2026-07-28T16:05:00Z"
		updatedAt   = "2026-07-31T14:00:00.000Z"
	)
	tests := []struct {
		name     string
		result   substack.Found
		expected string
	}{
		{
			name: "scheduled",
			result: substack.Found{Found: true, Post: &substack.Post{
				PostID:            "208706412",
				PostURL:           "https://gtmengineersearch.substack.com/publish/post/208706412",
				Status:            "scheduled",
				ScheduledAt:       stringPointer(scheduledAt),
				DraftUpdatedAt:    stringPointer(updatedAt),
				CorrelationMarker: marker,
			}},
			expected: `{"found":true,"post":{
				"post_id":"208706412",
				"post_url":"https://gtmengineersearch.substack.com/publish/post/208706412",
				"status":"scheduled",
				"scheduled_at":"` + scheduledAt + `",
				"published_at":null,
				"draft_updated_at":"` + updatedAt + `",
				"correlation_marker":"` + marker + `"
			}}`,
		},
		{
			name: "published",
			result: substack.Found{Found: true, Post: &substack.Post{
				PostID:            "208706412",
				PostURL:           "https://gtmengineersearch.substack.com/publish/post/208706412",
				Status:            "published",
				ScheduledAt:       stringPointer(scheduledAt),
				PublishedAt:       stringPointer(publishedAt),
				CorrelationMarker: marker,
			}},
			expected: `{"found":true,"post":{
				"post_id":"208706412",
				"post_url":"https://gtmengineersearch.substack.com/publish/post/208706412",
				"status":"published",
				"scheduled_at":"` + scheduledAt + `",
				"published_at":"` + publishedAt + `",
				"draft_updated_at":null,
				"correlation_marker":"` + marker + `"
			}}`,
		},
		{
			name:     "deleted",
			result:   substack.Found{Found: false},
			expected: `{"found":false}`,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			output, err := execute(
				t,
				authenticatedOptions(&fakeService{getResult: test.result}),
				"posts",
				"get",
				"--publication",
				"gtmengineersearch",
				"--post-id",
				"208706412",
				"--json",
			)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			assertJSONEqual(t, output, test.expected)
		})
	}
}

func TestCommandFailureDoesNotLeakCookie(t *testing.T) {
	t.Parallel()

	const cookie = "connect.sid=command-boundary-secret"
	fake := &fakeService{
		findError: &substack.HTTPError{
			StatusCode: 403,
			Path:       "/api/v1/post_management/drafts",
		},
	}
	options := authenticatedOptions(fake)
	options.LookupEnv = func(string) (string, bool) { return cookie, true }
	output, err := execute(
		t,
		options,
		"drafts",
		"find",
		"--publication",
		"gtmengineersearch",
		"--correlation-marker",
		"gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d",
		"--json",
	)
	if err == nil {
		t.Fatal("Execute() error = nil, want permission failure")
	}
	if strings.Contains(output, cookie) || strings.Contains(err.Error(), cookie) {
		t.Fatalf("command failure leaked cookie: output=%q error=%v", output, err)
	}
	if cli.ExitCode(err) != 3 {
		t.Fatalf("ExitCode() = %d, want auth exit 3", cli.ExitCode(err))
	}
}

func TestAuthenticatedCommandsFailBeforeServiceWhenCookieMissing(t *testing.T) {
	t.Parallel()

	called := false
	_, err := execute(
		t,
		cli.Options{
			Version:   "0.1.0",
			LookupEnv: func(string) (string, bool) { return "", false },
			NewService: func(string, string) (cli.Service, error) {
				called = true
				return nil, errors.New("must not run")
			},
		},
		"drafts",
		"find",
		"--publication",
		"gtmengineersearch",
		"--correlation-marker",
		"gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d",
		"--json",
	)
	if err == nil || !strings.Contains(err.Error(), "PP_SUBSTACK_SESSION_COOKIE") {
		t.Fatalf("error = %v, want missing-cookie error", err)
	}
	if called {
		t.Fatal("service factory was called without a cookie")
	}
}

func TestDraftsCreateRejectsMarkerThatFindCannotReconcile(t *testing.T) {
	t.Parallel()

	called := false
	_, err := execute(
		t,
		cli.Options{
			Version: "0.1.0",
			LookupEnv: func(string) (string, bool) {
				return "connect.sid=session", true
			},
			NewService: func(string, string) (cli.Service, error) {
				called = true
				return &fakeService{}, nil
			},
		},
		"drafts",
		"create",
		"--publication",
		"gtmengineersearch",
		"--title",
		"GTM jobs",
		"--markdown-file",
		"/private/tmp/issue.md",
		"--correlation-marker",
		"arbitrary-marker",
		"--json",
	)
	if err == nil || !strings.Contains(err.Error(), "gtme-issue:<uuid>") {
		t.Fatalf("error = %v, want marker-format error", err)
	}
	if called {
		t.Fatal("service was called with an unreconcilable marker")
	}
}

func TestAutomationCommandsRequireJSONFlag(t *testing.T) {
	t.Parallel()

	_, err := execute(
		t,
		authenticatedOptions(&fakeService{}),
		"posts",
		"get",
		"--publication",
		"gtmengineersearch",
		"--post-id",
		"208706412",
	)
	if err == nil || !strings.Contains(err.Error(), "--json is required") {
		t.Fatalf("error = %v, want JSON requirement", err)
	}
	if cli.ExitCode(err) != 2 {
		t.Fatalf("ExitCode() = %d, want usage exit 2", cli.ExitCode(err))
	}
}

func TestCommandTreeRejectsPositionalSecretsWithoutEcho(t *testing.T) {
	t.Parallel()

	const secret = "connect.sid=positional-secret"
	tests := []struct {
		name string
		args []string
	}{
		{name: "root", args: []string{secret}},
		{name: "drafts group", args: []string{"drafts", secret}},
		{name: "posts group", args: []string{"posts", secret}},
		{
			name: "draft update leaf",
			args: []string{
				"drafts",
				"update",
				"--publication",
				"gtmengineersearch",
				"--post-id",
				"208706412",
				"--title",
				"Updated GTM jobs this week",
				"--markdown-file",
				"/private/tmp/updated-issue.md",
				"--correlation-marker",
				"gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d",
				"--json",
				secret,
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			output, err := execute(
				t,
				cli.Options{Version: "0.1.0"},
				test.args...,
			)
			if err == nil {
				t.Fatal("Execute() error = nil, want positional rejection")
			}
			if strings.Contains(output, secret) ||
				strings.Contains(err.Error(), secret) {
				t.Fatalf(
					"positional argument leaked: output=%q error=%v",
					output,
					err,
				)
			}
			if cli.ExitCode(err) != 2 {
				t.Fatalf(
					"ExitCode() = %d, want usage exit 2",
					cli.ExitCode(err),
				)
			}
		})
	}
}

func TestHelpDescribesDraftUpdates(t *testing.T) {
	t.Parallel()

	root := cli.NewRoot(cli.Options{Version: "0.1.0"})
	if !strings.Contains(strings.ToLower(root.Short), "update") {
		t.Fatalf("root Short = %q, want update", root.Short)
	}
	drafts, _, err := root.Find([]string{"drafts"})
	if err != nil {
		t.Fatalf("Find(drafts) error = %v", err)
	}
	if !strings.Contains(strings.ToLower(drafts.Short), "update") {
		t.Fatalf("drafts Short = %q, want update", drafts.Short)
	}
}

func TestExitCodeClassifiesAuthRemoteAndContractFailures(t *testing.T) {
	t.Parallel()

	_, missingCookie := execute(
		t,
		cli.Options{
			Version:   "0.1.0",
			LookupEnv: func(string) (string, bool) { return "", false },
		},
		"drafts",
		"find",
		"--publication",
		"gtmengineersearch",
		"--correlation-marker",
		"gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d",
		"--json",
	)
	if cli.ExitCode(missingCookie) != 3 {
		t.Fatalf("missing-cookie exit = %d, want 3", cli.ExitCode(missingCookie))
	}
	if cli.ExitCode(&substack.HTTPError{StatusCode: 403, Path: "/api/v1/drafts"}) != 3 {
		t.Fatal("HTTP 403 did not map to auth exit 3")
	}
	if cli.ExitCode(&substack.HTTPError{StatusCode: 429, Path: "/api/v1/drafts"}) != 5 {
		t.Fatal("HTTP 429 did not map to remote exit 5")
	}
	if cli.ExitCode(errors.New("malformed response")) != 7 {
		t.Fatal("unclassified failure did not map to contract exit 7")
	}
}

type fakeService struct {
	createResult  substack.Draft
	createTitle   string
	createBody    string
	updateResult  substack.UpdatedDraft
	updateCalls   int
	updatePostID  string
	updateTitle   string
	updateBody    string
	updateMarker  string
	updateError   error
	compareResult substack.DraftComparison
	compareCalls  int
	comparePostID string
	compareTitle  string
	compareBody   string
	compareMarker string
	findResult    substack.Found
	findError     error
	getResult     substack.Found
	getError      error
}

func (fake *fakeService) CreateDraft(
	_ context.Context,
	title string,
	body string,
	_ string,
) (substack.Draft, error) {
	fake.createTitle = title
	fake.createBody = body
	return fake.createResult, nil
}

func (fake *fakeService) UpdateDraft(
	_ context.Context,
	postID string,
	title string,
	body string,
	correlationMarker string,
) (substack.UpdatedDraft, error) {
	fake.updateCalls++
	fake.updatePostID = postID
	fake.updateTitle = title
	fake.updateBody = body
	fake.updateMarker = correlationMarker
	return fake.updateResult, fake.updateError
}

func (fake *fakeService) CompareDraft(
	_ context.Context,
	postID string,
	title string,
	body string,
	correlationMarker string,
) (substack.DraftComparison, error) {
	fake.compareCalls++
	fake.comparePostID = postID
	fake.compareTitle = title
	fake.compareBody = body
	fake.compareMarker = correlationMarker
	return fake.compareResult, nil
}

func (fake *fakeService) FindByMarker(
	_ context.Context,
	_ string,
) (substack.Found, error) {
	return fake.findResult, fake.findError
}

func (fake *fakeService) GetPost(
	_ context.Context,
	_ string,
) (substack.Found, error) {
	return fake.getResult, fake.getError
}

func authenticatedOptions(service cli.Service) cli.Options {
	return cli.Options{
		Version: "0.1.0",
		LookupEnv: func(name string) (string, bool) {
			if name == "PP_SUBSTACK_SESSION_COOKIE" {
				return "connect.sid=session", true
			}
			return "", false
		},
		NewService: func(string, string) (cli.Service, error) {
			return service, nil
		},
	}
}

func stringPointer(value string) *string {
	return &value
}

func execute(t *testing.T, options cli.Options, args ...string) (string, error) {
	t.Helper()
	var stdout bytes.Buffer
	command := cli.NewRoot(options)
	command.SetArgs(args)
	command.SetOut(&stdout)
	command.SetErr(&bytes.Buffer{})
	err := command.Execute()
	return stdout.String(), err
}

func assertJSONEqual(t *testing.T, actual string, expected string) {
	t.Helper()
	var actualValue any
	if err := json.Unmarshal([]byte(actual), &actualValue); err != nil {
		t.Fatalf("actual is not JSON: %v; output = %q", err, actual)
	}
	var expectedValue any
	if err := json.Unmarshal([]byte(expected), &expectedValue); err != nil {
		t.Fatalf("expected fixture is not JSON: %v", err)
	}
	actualEncoded, _ := json.Marshal(actualValue)
	expectedEncoded, _ := json.Marshal(expectedValue)
	if !bytes.Equal(actualEncoded, expectedEncoded) {
		t.Fatalf("actual = %s, want %s", actualEncoded, expectedEncoded)
	}
}
