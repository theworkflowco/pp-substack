package markdown

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strings"
)

type node struct {
	Type    string         `json:"type"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Content []node         `json:"content,omitempty"`
	Text    string         `json:"text,omitempty"`
	Marks   []mark         `json:"marks,omitempty"`
}

type mark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

func ToProseMirror(source string, correlationMarker string) (string, error) {
	if strings.Count(source, correlationMarker) != 1 {
		return "", fmt.Errorf("correlation marker must appear exactly once in Markdown")
	}

	lines := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
	content := make([]node, 0, len(lines))
	for index := 0; index < len(lines); {
		line := strings.TrimSpace(lines[index])
		if line == "" {
			index++
			continue
		}

		if level, text, ok := heading(line); ok {
			inline, err := parseInline(text)
			if err != nil {
				return "", err
			}
			// Substack renders low heading levels very large in posts and
			// emails; the publication's approved hierarchy steps every
			// Markdown heading down one level (## -> h3, ### -> h4).
			rendered := level + 1
			if rendered > 6 {
				rendered = 6
			}
			content = append(content, node{
				Type:    "heading",
				Attrs:   map[string]any{"level": rendered},
				Content: inline,
			})
			index++
			continue
		}

		if line == "---" {
			content = append(content, node{Type: "horizontal_rule"})
			index++
			continue
		}

		if caption, ok := strings.CutPrefix(line, "::subscribe::"); ok {
			caption = strings.TrimSpace(caption)
			if caption == "" {
				return "", fmt.Errorf("subscribe directive requires a caption")
			}
			inline, err := parseInline(caption)
			if err != nil {
				return "", err
			}
			content = append(content, node{
				Type: "subscribeWidget",
				Attrs: map[string]any{
					"url":      "%%checkout_url%%",
					"text":     "Subscribe",
					"language": "en",
				},
				Content: []node{{Type: "ctaCaption", Content: inline}},
			})
			index++
			continue
		}

		if strings.HasPrefix(line, ">") {
			paragraphs := make([]node, 0)
			var pending []string
			flush := func() error {
				if len(pending) == 0 {
					return nil
				}
				inline, err := parseInline(strings.Join(pending, " "))
				if err != nil {
					return err
				}
				paragraphs = append(paragraphs, node{
					Type:    "paragraph",
					Attrs:   map[string]any{"textAlign": nil},
					Content: inline,
				})
				pending = nil
				return nil
			}
			for index < len(lines) {
				quoted := strings.TrimSpace(lines[index])
				if !strings.HasPrefix(quoted, ">") {
					break
				}
				text := strings.TrimSpace(strings.TrimPrefix(quoted, ">"))
				if text == "" {
					if err := flush(); err != nil {
						return "", err
					}
				} else {
					pending = append(pending, text)
				}
				index++
			}
			if err := flush(); err != nil {
				return "", err
			}
			if len(paragraphs) == 0 {
				return "", fmt.Errorf("blockquote must contain text")
			}
			content = append(content, node{Type: "calloutBlock", Content: paragraphs})
			continue
		}

		if strings.HasPrefix(line, "- ") {
			items := make([]node, 0)
			for index < len(lines) {
				itemLine := strings.TrimSpace(lines[index])
				if !strings.HasPrefix(itemLine, "- ") {
					break
				}
				inline, err := parseInline(strings.TrimPrefix(itemLine, "- "))
				if err != nil {
					return "", err
				}
				items = append(items, node{
					Type: "list_item",
					Content: []node{{
						Type:    "paragraph",
						Attrs:   map[string]any{"textAlign": nil},
						Content: inline,
					}},
				})
				index++
			}
			content = append(content, node{Type: "bullet_list", Content: items})
			continue
		}

		paragraphLines := []string{line}
		index++
		for index < len(lines) {
			next := strings.TrimSpace(lines[index])
			if next == "" {
				break
			}
			if _, _, ok := heading(next); ok ||
				strings.HasPrefix(next, "- ") ||
				strings.HasPrefix(next, ">") ||
				strings.HasPrefix(next, "::subscribe::") ||
				next == "---" {
				break
			}
			paragraphLines = append(paragraphLines, next)
			index++
		}
		inline, err := parseInline(strings.Join(paragraphLines, " "))
		if err != nil {
			return "", err
		}
		content = append(content, node{
			Type:    "paragraph",
			Attrs:   map[string]any{"textAlign": nil},
			Content: inline,
		})
	}

	encoded, err := json.Marshal(node{Type: "doc", Content: content})
	if err != nil {
		return "", fmt.Errorf("encode ProseMirror document: %w", err)
	}
	if strings.Count(string(encoded), correlationMarker) != 1 {
		return "", fmt.Errorf("correlation marker must survive conversion exactly once")
	}
	return string(encoded), nil
}

func heading(line string) (int, string, bool) {
	for level := 1; level <= 6; level++ {
		prefix := strings.Repeat("#", level) + " "
		if strings.HasPrefix(line, prefix) {
			return level, strings.TrimPrefix(line, prefix), true
		}
	}
	return 0, "", false
}

func parseInline(text string) ([]node, error) {
	nodes := make([]node, 0)
	for len(text) > 0 {
		switch {
		case strings.HasPrefix(text, "**"):
			end := indexUnescaped(text[2:], "**")
			if end < 0 {
				return nil, fmt.Errorf("unclosed strong emphasis in Markdown")
			}
			inner, err := parseInlineWithMark(text[2:2+end], mark{Type: "strong"})
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, inner...)
			text = text[2+end+2:]
		case strings.HasPrefix(text, "_"):
			end := indexUnescaped(text[1:], "_")
			if end < 0 {
				return nil, fmt.Errorf("unclosed emphasis in Markdown")
			}
			inner, err := parseInlineWithMark(text[1:1+end], mark{Type: "em"})
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, inner...)
			text = text[1+end+1:]
		case strings.HasPrefix(text, "["):
			labelEnd := strings.Index(text, "](")
			if labelEnd < 0 {
				nodes = appendText(nodes, unescape(text[:1]))
				text = text[1:]
				continue
			}
			targetEnd := strings.Index(text[labelEnd+2:], ")")
			if targetEnd < 0 {
				return nil, fmt.Errorf("unclosed Markdown link")
			}
			target := text[labelEnd+2 : labelEnd+2+targetEnd]
			if strings.HasPrefix(target, "<") && strings.HasSuffix(target, ">") {
				target = strings.TrimSuffix(strings.TrimPrefix(target, "<"), ">")
			}
			parsed, err := url.Parse(target)
			if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
				return nil, fmt.Errorf("Markdown link must use an absolute HTTP(S) URL")
			}
			nodes = append(nodes, node{
				Type: "text",
				Text: unescape(text[1:labelEnd]),
				Marks: []mark{{
					Type:  "link",
					Attrs: map[string]any{"href": target},
				}},
			})
			text = text[labelEnd+2+targetEnd+1:]
		default:
			next := nextInlineStart(text)
			nodes = appendText(nodes, unescape(text[:next]))
			text = text[next:]
		}
	}
	return nodes, nil
}

func parseInlineWithMark(text string, outer mark) ([]node, error) {
	inner, err := parseInline(text)
	if err != nil {
		return nil, err
	}
	for index := range inner {
		inner[index].Marks = append(inner[index].Marks, outer)
	}
	return inner, nil
}

func nextInlineStart(text string) int {
	next := len(text)
	for _, token := range []string{"**", "["} {
		if index := indexUnescaped(text[1:], token); index >= 0 && index+1 < next {
			next = index + 1
		}
	}
	if index := emphasisStart(text[1:]); index >= 0 && index+1 < next {
		next = index + 1
	}
	if next == 0 {
		return 1
	}
	return next
}

func emphasisStart(text string) int {
	for offset := 0; offset < len(text); {
		index := indexUnescaped(text[offset:], "_")
		if index < 0 {
			return -1
		}
		index += offset
		if indexUnescaped(text[index+1:], "_") >= 0 {
			return index
		}
		offset = index + 1
	}
	return -1
}

func indexUnescaped(text string, token string) int {
	for offset := 0; offset <= len(text)-len(token); {
		index := strings.Index(text[offset:], token)
		if index < 0 {
			return -1
		}
		index += offset
		backslashes := 0
		for position := index - 1; position >= 0 && text[position] == '\\'; position-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return index
		}
		offset = index + len(token)
	}
	return -1
}

func appendText(nodes []node, text string) []node {
	if text == "" {
		return nodes
	}
	return append(nodes, node{Type: "text", Text: text})
}

func unescape(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))
	escaped := false
	for _, character := range text {
		if escaped {
			builder.WriteRune(character)
			escaped = false
			continue
		}
		if character == '\\' {
			escaped = true
			continue
		}
		builder.WriteRune(character)
	}
	if escaped {
		builder.WriteByte('\\')
	}
	return html.UnescapeString(builder.String())
}
