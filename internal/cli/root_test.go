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

func TestRootExposesOnlyApprovedCommandSurface(t *testing.T) {
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

	expected := []string{"drafts create", "drafts find", "posts get", "version"}
	if strings.Join(paths, ",") != strings.Join(expected, ",") {
		t.Fatalf("leaf commands = %v, want %v", paths, expected)
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
	fake := &fakeService{
		findResult: substack.Found{
			Found: true,
			Post: &substack.Post{
				PostID:            "208706412",
				PostURL:           "https://gtmengineersearch.substack.com/publish/post/208706412",
				Status:            "draft",
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
			"correlation_marker":"`+marker+`"
		}}`,
	)
}

func TestPostsGetReturnsFoundEnvelope(t *testing.T) {
	t.Parallel()

	const marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
	fake := &fakeService{
		getResult: substack.Found{
			Found: true,
			Post: &substack.Post{
				PostID:            "208706412",
				PostURL:           "https://gtmengineersearch.substack.com/publish/post/208706412",
				Status:            "draft",
				ScheduledAt:       nil,
				PublishedAt:       nil,
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
				CorrelationMarker: marker,
			}},
			expected: `{"found":true,"post":{
				"post_id":"208706412",
				"post_url":"https://gtmengineersearch.substack.com/publish/post/208706412",
				"status":"scheduled",
				"scheduled_at":"` + scheduledAt + `",
				"published_at":null,
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

func TestLeafCommandsRejectPositionalArguments(t *testing.T) {
	t.Parallel()

	_, err := execute(
		t,
		cli.Options{Version: "0.1.0"},
		"version",
		"--json",
		"ignored",
	)
	if err == nil || !strings.Contains(err.Error(), "unknown command") &&
		!strings.Contains(err.Error(), "arg") {
		t.Fatalf("error = %v, want positional-argument rejection", err)
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
	createResult substack.Draft
	createTitle  string
	createBody   string
	findResult   substack.Found
	findError    error
	getResult    substack.Found
	getError     error
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
