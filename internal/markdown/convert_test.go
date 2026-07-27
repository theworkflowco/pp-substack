package markdown_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/theworkflowco/pp-substack/internal/markdown"
)

func TestToProseMirrorPreservesNewsletterStructureAndMarkerOnce(t *testing.T) {
	t.Parallel()

	const marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
	input := strings.Join([]string{
		"# GTM Engineer Search",
		"",
		"## Top picks",
		"",
		"- **Domains:** Outbound, Internal Tools",
		"- **Sources:** [Apply now](https://jobs.example/apply)",
		"",
		"Issue reference: " + marker,
		"",
	}, "\n")

	body, err := markdown.ToProseMirror(input, marker)
	if err != nil {
		t.Fatalf("ToProseMirror() error = %v", err)
	}
	if strings.Count(body, marker) != 1 {
		t.Fatalf("marker count = %d, want 1; body = %s", strings.Count(body, marker), body)
	}

	var document struct {
		Type    string `json:"type"`
		Content []struct {
			Type  string `json:"type"`
			Attrs struct {
				Level int `json:"level"`
			} `json:"attrs"`
			Content []json.RawMessage `json:"content"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(body), &document); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if document.Type != "doc" {
		t.Fatalf("document type = %q, want doc", document.Type)
	}
	if len(document.Content) != 4 {
		t.Fatalf("top-level node count = %d, want 4", len(document.Content))
	}
	if document.Content[0].Type != "heading" || document.Content[0].Attrs.Level != 1 {
		t.Fatalf("first node = %#v, want level-1 heading", document.Content[0])
	}
	if document.Content[1].Type != "heading" || document.Content[1].Attrs.Level != 2 {
		t.Fatalf("second node = %#v, want level-2 heading", document.Content[1])
	}
	if document.Content[2].Type != "bullet_list" {
		t.Fatalf("third node type = %q, want bullet_list", document.Content[2].Type)
	}
	if document.Content[3].Type != "paragraph" {
		t.Fatalf("last node type = %q, want paragraph", document.Content[3].Type)
	}
}

func TestToProseMirrorRejectsMarkerMissingFromMarkdown(t *testing.T) {
	t.Parallel()

	_, err := markdown.ToProseMirror(
		"# GTM Engineer Search\n",
		"gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d",
	)
	if err == nil || !strings.Contains(err.Error(), "exactly once") {
		t.Fatalf("error = %v, want marker-count error", err)
	}
}
