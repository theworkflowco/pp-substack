package markdown_test

import (
	"encoding/json"
	"os"
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
		"- **Sources:** [Apply now](<https://jobs.example/apply>)",
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

func TestToProseMirrorPreservesExactComposerMarkdownSubset(t *testing.T) {
	t.Parallel()

	const marker = "gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d"
	input := strings.Join([]string{
		"# GTME Jobs Roundup — July 20, 2026",
		"",
		"_Cadence 2026-W29 · assembled through 2026-07-20T00:00:00.000Z._",
		"",
		"## Top picks",
		"",
		"#### Acme \\[Labs\\] \\*North\\* — Founding GTM Engineer \\(AI\\)",
		"",
		"- **Domains:** Enrichment &amp; Intelligence",
		"- **Sources:** [Listing 1](<https://jobs.example/acme>)",
		"",
		"---",
		"",
		"Issue reference: " + marker,
	}, "\n")

	body, err := markdown.ToProseMirror(input, marker)
	if err != nil {
		t.Fatalf("ToProseMirror() error = %v", err)
	}

	var document struct {
		Content []struct {
			Type    string         `json:"type"`
			Attrs   map[string]any `json:"attrs"`
			Content []struct {
				Text  string `json:"text"`
				Marks []struct {
					Type string `json:"type"`
				} `json:"marks"`
			} `json:"content"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(body), &document); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}

	var foundLevelFour bool
	var foundItalicCadence bool
	var foundRule bool
	for _, block := range document.Content {
		if block.Type == "heading" && block.Attrs["level"] == float64(4) {
			foundLevelFour = true
		}
		if block.Type == "horizontal_rule" {
			foundRule = true
		}
		for _, inline := range block.Content {
			if strings.Contains(inline.Text, "Cadence 2026-W29") &&
				len(inline.Marks) == 1 &&
				inline.Marks[0].Type == "em" {
				foundItalicCadence = true
			}
		}
	}
	if !foundLevelFour {
		t.Error("composer level-four listing heading was not preserved")
	}
	if !foundItalicCadence {
		t.Error("composer italic cadence line was not preserved")
	}
	if !foundRule {
		t.Error("composer horizontal rule was not preserved")
	}
	if strings.Contains(body, "&amp;") {
		t.Fatalf("HTML entity was rendered literally: %s", body)
	}
	if !strings.Contains(body, `"text":" Enrichment \u0026 Intelligence"`) {
		t.Fatalf("decoded entity missing from body: %s", body)
	}
}

func TestToProseMirrorConvertsExactSmallComposerSnapshot(t *testing.T) {
	t.Parallel()

	const marker = "gtme-issue:00000000-0000-4000-8000-000000000100"
	source, err := os.ReadFile("testdata/newsletter-composer-small.md")
	if err != nil {
		t.Fatalf("read exact composer snapshot: %v", err)
	}
	body, err := markdown.ToProseMirror(string(source), marker)
	if err != nil {
		t.Fatalf("ToProseMirror(exact composer snapshot) error = %v", err)
	}
	if strings.Count(body, marker) != 1 {
		t.Fatalf("marker count = %d, want 1", strings.Count(body, marker))
	}
	if strings.Contains(body, `\\`) || strings.Contains(body, "&amp;") {
		t.Fatalf("composer escapes leaked into rendered body: %s", body)
	}
	if !strings.Contains(body, `"type":"horizontal_rule"`) ||
		!strings.Contains(body, `"level":4`) ||
		!strings.Contains(body, `"type":"em"`) {
		t.Fatalf("composer structure missing from rendered body: %s", body)
	}
}
