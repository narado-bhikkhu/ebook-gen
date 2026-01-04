package main

import (
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"strings"
)

// buildHTML constructs the full HTML string for given terms. If onTerm is non-nil it will be
// called after each term is appended so callers can track progress.
func (eg *EbookGenerator) buildHTML(terms []TermData, onTerm func()) string {
	// Try to read the embedded Tibetan font and encode as base64 for @font-face
	fontFace := ""
	if fontData, ferr := ioutil.ReadFile(filepath.Join(filepath.Dir(eg.inputDir), "DDC_Uchen-webfont.woff")); ferr == nil {
		b64 := base64.StdEncoding.EncodeToString(fontData)
		fontFace = fmt.Sprintf("@font-face { font-family: 'DDC Uchen'; src: url('data:font/woff;base64,%s') format('woff'); }", b64)
	} else if fontData, ferr := ioutil.ReadFile("DDC_Uchen-webfont.woff"); ferr == nil {
		b64 := base64.StdEncoding.EncodeToString(fontData)
		fontFace = fmt.Sprintf("@font-face { font-family: 'DDC Uchen'; src: url('data:font/woff;base64,%s') format('woff'); }", b64)
	}

	var sb strings.Builder
	sb.WriteString("<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>")
	sb.WriteString(escapeXML(eg.title))
	sb.WriteString("</title><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><style>")
	sb.WriteString(fontFace)
	sb.WriteString(`
        body{font-family: Georgia, serif; margin: 24px; color:#111}
        h1,h2{color:#222}
        .definition-table{width:100%; border-collapse:collapse; margin-bottom:1.2em}
        .definition-table td{vertical-align:top; padding:6px; border-bottom:1px solid #eee}
        .dictName{font-weight:700; color:#3b3}
        .tib{font-family: 'DDC Uchen', Jomolhari, Arial, sans-serif}
        .wylie{font-family: monospace}

        .term{margin-bottom:2.5em; padding-bottom:1em}
        .term-sep{border:0; border-top:1px solid #ddd; margin:2.5em 0}
    `)
	sb.WriteString("</style></head><body>")
	sb.WriteString("<h1>")
	sb.WriteString(escapeXML(eg.title))
	sb.WriteString("</h1>")

	for i, term := range terms {
		display := term.SearchTerm
		if display == "" {
			display = term.SearchTermWylie
		}
		sb.WriteString("<div class=\"term\">\n")
		sb.WriteString(fmt.Sprintf("<h2 class=\"tib\">%s <span class=\"wylie\">(%s)</span></h2>\n", escapeXML(display), escapeXML(term.SearchTermWylie)))

		if term.DefinitionsCount > 0 {
			sb.WriteString("<table class=\"definition-table\"><tbody>\n")
			for dictName, def := range term.Definitions {
				if def == "" {
					continue
				}
				defProcessed := wrapTibetanHTML(formatDefinitionText(def))
				sb.WriteString(fmt.Sprintf("<tr><td class=\"dictName\">%s</td><td class=\"definition\">%s</td></tr>\n", escapeXML(dictName), defProcessed))
			}
			sb.WriteString("</tbody></table>\n")
		}

		if term.RelatedTermsCount > 0 {
			sb.WriteString("<div class=\"related\"><strong>Related:</strong><ul>\n")
			for _, rt := range term.RelatedTerms {
				sb.WriteString(fmt.Sprintf("<li><span class=\"tib\">%s</span> <span class=\"wylie\">(%s)</span></li>\n", escapeXML(rt.Unicode), escapeXML(rt.Wylie)))
			}
			sb.WriteString("</ul></div>\n")
		}

		sb.WriteString(fmt.Sprintf("<div class=\"meta\">Term #%d — Definitions: %d — Related: %d</div>\n", i+1, term.DefinitionsCount, term.RelatedTermsCount))

		if i < len(terms)-1 {
			sb.WriteString("</div>\n<hr class=\"term-sep\"/>\n")
		} else {
			sb.WriteString("</div>\n")
		}

		if onTerm != nil {
			onTerm()
		}
	}

	sb.WriteString("</body></html>")
	return sb.String()
}

// GenerateHTML writes the built HTML to the output file path set on the generator.
func (eg *EbookGenerator) GenerateHTML(terms []TermData) error {
	// Track progress but don't print here; caller may already display progress.
	html := eg.buildHTML(terms, nil)
	if eg.outputFile == "" {
		return fmt.Errorf("no output file specified for HTML generation")
	}
	if err := ioutil.WriteFile(eg.outputFile, []byte(html), 0644); err != nil {
		return fmt.Errorf("failed to write HTML output: %w", err)
	}
	fmt.Printf("✅ HTML written: %s (from %d terms)\n", eg.outputFile, len(terms))
	return nil
}
