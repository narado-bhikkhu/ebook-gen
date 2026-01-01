package main

import (
	"archive/zip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// TermData represents a single term's export from the Tibetan Dictionary CLI
type TermData struct {
	SearchTerm         string `json:"searchTerm"`
	SearchTermWylie    string
	Timestamp          string            `json:"timestamp"`
	Definitions        map[string]string `json:"definitions"`
	DefinitionsWylie   map[string]string `json:"definitionsWylie"`
	DefinitionsUnicode map[string]string `json:"definitionsUnicode"`
	RelatedTerms       []RelatedTerm     `json:"relatedTerms"`
	DefinitionsCount   int               `json:"definitionsCount"`
	RelatedTermsCount  int               `json:"relatedTermsCount"`
}

// RelatedTerm represents a related term with both Wylie and Unicode forms
type RelatedTerm struct {
	Wylie   string `json:"wylie"`
	Unicode string `json:"unicode"`
}

// EbookGenerator creates an EPUB/AZW ebook from JSON term files
type EbookGenerator struct {
	inputDir   string
	outputFile string
	title      string
	author     string
}

// NewEbookGenerator creates a new ebook generator
func NewEbookGenerator(inputDir, outputFile, title, author string) *EbookGenerator {
	return &EbookGenerator{
		inputDir:   inputDir,
		outputFile: outputFile,
		title:      title,
		author:     author,
	}
}

// AggregatedTermExport represents the structure of a single-mode export file
type AggregatedTermExport struct {
	Timestamp  string                 `json:"timestamp"`
	TotalTerms int                    `json:"totalTerms"`
	Terms      map[string]TermData    `json:"terms"`
	Summary    map[string]interface{} `json:"summary"`
}

// ReadTermFiles reads all JSON term files from the input directory
// Supports both per-term format and aggregated single-file format
func (eg *EbookGenerator) ReadTermFiles() ([]TermData, error) {
	files, err := ioutil.ReadDir(eg.inputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory %s: %w", eg.inputDir, err)
	}

	var terms []TermData
	jsonFiles := []string{}

	// Collect all JSON files
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".json") {
			jsonFiles = append(jsonFiles, filepath.Join(eg.inputDir, file.Name()))
		}
	}

	if len(jsonFiles) == 0 {
		return nil, fmt.Errorf("no JSON files found in %s", eg.inputDir)
	}

	// Try per-term format first. Also support "paged" per-file format where
	// `searchTerm` is an object and `definitions` entries include wylie/unicode.
	for _, path := range jsonFiles {
		data, err := ioutil.ReadFile(path)
		if err != nil {
			continue
		}

		var term TermData
		if err := json.Unmarshal(data, &term); err == nil && term.SearchTerm != "" {
			terms = append(terms, term)
			continue
		}

		// Try paged-style JSON (example: searchTerm is an object with wylie/unicode)
		var paged struct {
			Timestamp  string `json:"timestamp"`
			SearchTerm struct {
				Wylie   string `json:"wylie"`
				Unicode string `json:"unicode"`
			} `json:"searchTerm"`
			Definitions       map[string]interface{} `json:"definitions"`
			RelatedTerms      []RelatedTerm          `json:"relatedTerms"`
			DefinitionsCount  int                    `json:"definitionsCount"`
			RelatedTermsCount int                    `json:"relatedTermsCount"`
		}

		if err := json.Unmarshal(data, &paged); err == nil && (paged.SearchTerm.Unicode != "" || paged.SearchTerm.Wylie != "") {
			// Build display string: Unicode ( Wylie )
			displayTerm := paged.SearchTerm.Unicode
			if displayTerm == "" {
				displayTerm = paged.SearchTerm.Wylie
			}
			if paged.SearchTerm.Wylie != "" && paged.SearchTerm.Unicode != "" {
				displayTerm = fmt.Sprintf("%s (%s)", paged.SearchTerm.Unicode, paged.SearchTerm.Wylie)
			}

			t := TermData{
				SearchTerm:        paged.SearchTerm.Unicode,
				SearchTermWylie:   paged.SearchTerm.Wylie,
				Timestamp:         paged.Timestamp,
				Definitions:       make(map[string]string),
				RelatedTerms:      paged.RelatedTerms,
				DefinitionsCount:  paged.DefinitionsCount,
				RelatedTermsCount: paged.RelatedTermsCount,
			}

			for k, v := range paged.Definitions {
				// Handle both string and object definitions
				switch val := v.(type) {
				case string:
					t.Definitions[k] = val
				case map[string]interface{}:
					uni := ""
					w := ""
					if u, ok := val["unicode"].(string); ok {
						uni = u
					}
					if wy, ok := val["wylie"].(string); ok {
						w = wy
					}
					defDisplay := uni
					if defDisplay == "" {
						defDisplay = w
					}
					if uni != "" && w != "" {
						defDisplay = fmt.Sprintf("%s (%s)", uni, w)
					}
					t.Definitions[k] = defDisplay
				default:
					t.Definitions[k] = fmt.Sprintf("%v", val)
				}
			}

			terms = append(terms, t)
		}
	}

	// If no per-term files found, try aggregated format
	if len(terms) == 0 {
		for _, path := range jsonFiles {
			data, err := ioutil.ReadFile(path)
			if err != nil {
				continue
			}

			var agg AggregatedTermExport
			if err := json.Unmarshal(data, &agg); err == nil && len(agg.Terms) > 0 {
				for key, term := range agg.Terms {
					// Use the key as searchTerm if not already set
					if term.SearchTerm == "" {
						term.SearchTerm = key
					}
					terms = append(terms, term)
				}
				break // Only process first aggregated file
			}
		}
	}

	if len(terms) == 0 {
		return nil, fmt.Errorf("no valid term data found in %s (tried per-term and aggregated formats)", eg.inputDir)
	}

	// Sort terms alphabetically
	sort.Slice(terms, func(i, j int) bool {
		return terms[i].SearchTerm < terms[j].SearchTerm
	})

	return terms, nil
}

// GenerateEPUB generates an EPUB file from the term data
func (eg *EbookGenerator) GenerateEPUB(terms []TermData) error {
	// Create EPUB as ZIP archive
	zipFile, err := os.Create(eg.outputFile)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer zipFile.Close()

	writer := zip.NewWriter(zipFile)
	defer writer.Close()

	// Write mimetype file (uncompressed, must be first)
	mimetypeFile, err := writer.Create("mimetype")
	if err != nil {
		return err
	}
	io.WriteString(mimetypeFile, "application/epub+zip")

	// Write container.xml
	if err := eg.writeContainerXML(writer); err != nil {
		return err
	}

	// Write content.opf (package file)
	if err := eg.writeContentOPF(writer, terms); err != nil {
		return err
	}

	// Write table of contents
	if err := eg.writeTOC(writer, terms); err != nil {
		return err
	}

	// Write title page
	if err := eg.writeTitlePage(writer); err != nil {
		return err
	}

	// Write term chapters
	if err := eg.writeTermChapters(writer, terms); err != nil {
		return err
	}

	// Write embedded font file
	if err := eg.embedFont(writer); err != nil {
		return err
	}

	fmt.Printf("✅ EPUB ebook created: %s\n", eg.outputFile)
	fmt.Printf("📖 Contains %d terms\n", len(terms))
	fmt.Println("\n📌 Note: EPUB is the open standard. To convert to AZW/AZW3:")
	fmt.Println("   - Use Calibre: calibre-ebook -i input.epub -o output.azw3")
	fmt.Println("   - Or use KindleGen: kindlegen input.epub -o output.mobi")

	return nil
}

// writeContainerXML writes the META-INF/container.xml file
func (eg *EbookGenerator) writeContainerXML(writer *zip.Writer) error {
	f, err := writer.Create("META-INF/container.xml")
	if err != nil {
		return err
	}

	containerXML := `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`

	_, err = io.WriteString(f, containerXML)
	return err
}

// writeContentOPF writes the OEBPS/content.opf (package) file
func (eg *EbookGenerator) writeContentOPF(writer *zip.Writer, terms []TermData) error {
	f, err := writer.Create("OEBPS/content.opf")
	if err != nil {
		return err
	}

	opf := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="uuid_id">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>%s</dc:title>
    <dc:creator opf:role="aut">%s</dc:creator>
    <dc:language>bo-en</dc:language>
    <dc:date>%s</dc:date>
    <dc:identifier id="uuid_id">tibetan-dict-ebook-%d</dc:identifier>
  </metadata>
  <manifest>
    <item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
    <item id="style" href="style.css" media-type="text/css"/>
    <item id="font" href="fonts/DDC_Uchen-webfont.woff" media-type="application/x-font-woff"/>
    <item id="title" href="title.xhtml" media-type="application/xhtml+xml"/>`, eg.title, eg.author, time.Now().Format("2006-01-02"), time.Now().Unix())

	// Add term chapters to manifest
	for i := range terms {
		opf += fmt.Sprintf("\n    <item id=\"chapter%d\" href=\"chapter%d.xhtml\" media-type=\"application/xhtml+xml\"/>", i+1, i+1)
	}

	opf += `
  </manifest>
  <spine toc="ncx">
    <itemref idref="title"/>
`

	// Add chapters to spine
	for i := range terms {
		opf += fmt.Sprintf("    <itemref idref=\"chapter%d\"/>\n", i+1)
	}

	opf += `  </spine>
  <guide>
    <reference type="toc" title="Table of Contents" href="toc.xhtml"/>
    <reference type="cover" title="Cover" href="title.xhtml"/>
  </guide>
</package>`

	_, err = io.WriteString(f, opf)
	return err
}

// writeTOC writes the OEBPS/toc.ncx file
func (eg *EbookGenerator) writeTOC(writer *zip.Writer, terms []TermData) error {
	f, err := writer.Create("OEBPS/toc.ncx")
	if err != nil {
		return err
	}

	toc := `<?xml version="1.0" encoding="UTF-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
  <head>
    <meta name="dtb:uid" content="tibetan-dict-ebook"/>
    <meta name="dtb:depth" content="1"/>
    <meta name="dtb:totalPageCount" content="0"/>
    <meta name="dtb:maxPageNumber" content="0"/>
  </head>
  <docTitle>
    <text>Tibetan Dictionary</text>
  </docTitle>
  <navMap>
    <navPoint id="title" playOrder="1">
      <navLabel><text>Title</text></navLabel>
      <content src="title.xhtml"/>
    </navPoint>
`

	for i, term := range terms {
		toc += fmt.Sprintf("    <navPoint id=\"chapter%d\" playOrder=\"%d\">\n", i+1, i+2)
		toc += fmt.Sprintf("      <navLabel><text>%s</text></navLabel>\n", escapeXML(term.SearchTerm))
		toc += fmt.Sprintf("      <content src=\"chapter%d.xhtml\"/>\n", i+1)
		toc += "    </navPoint>\n"
	}

	toc += `  </navMap>
</ncx>`

	_, err = io.WriteString(f, toc)
	return err
}

// writeTitlePage writes the OEBPS/title.xhtml file
func (eg *EbookGenerator) writeTitlePage(writer *zip.Writer) error {
	f, err := writer.Create("OEBPS/title.xhtml")
	if err != nil {
		return err
	}

	title := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.1//EN" "http://www.w3.org/TR/xhtml11/DTD/xhtml11.dtd">
<html xmlns="http://www.w3.org/1999/xhtml">
  <head>
    <title>%s</title>
    <link rel="stylesheet" type="text/css" href="style.css"/>
  </head>
  <body>
    <h1>%s</h1>
    <p class="author">By %s</p>
    <p class="timestamp">Generated: %s</p>
    <p class="description">A Tibetan-English dictionary with definitions and related terms.</p>
  </body>
</html>`, eg.title, eg.title, eg.author, time.Now().Format("January 2, 2006"))

	_, err = io.WriteString(f, title)
	return err
}

// writeTermChapters writes individual term chapter files
func (eg *EbookGenerator) writeTermChapters(writer *zip.Writer, terms []TermData) error {
	for i, term := range terms {
		chapterFile, err := writer.Create(fmt.Sprintf("OEBPS/chapter%d.xhtml", i+1))
		if err != nil {
			return err
		}

		chapter := eg.formatTermChapter(i+1, term)
		if _, err := io.WriteString(chapterFile, chapter); err != nil {
			return err
		}
	}

	// Write style.css
	styleFile, err := writer.Create("OEBPS/style.css")
	if err != nil {
		return err
	}

	style := `
@font-face {
  font-family: 'DDC Uchen';
  src: url('fonts/DDC_Uchen-webfont.woff') format('woff');
}

body {
  font-family: Georgia, serif;
  line-height: 1.6;
  margin: 1em;
  text-rendering: optimizeLegibility;
}

h1 {
  font-size: 1.8em;
  margin-top: 0.5em;
  margin-bottom: 0.3em;
  color: #333;
}

h2 {
  font-size: 1.3em;
  margin-top: 0.8em;
  margin-bottom: 0.3em;
  color: #555;
  border-bottom: 1px solid #ddd;
  padding-bottom: 0.2em;
}

.definition {
  margin-left: 1.5em;
  margin-bottom: 0.5em;
  padding: 0.5em;
  background-color: #f9f9f9;
  border-left: 3px solid #667eea;
}

.dict-name {
  font-weight: bold;
  color: #764ba2;
  font-size: 0.95em;
}

.related-terms {
  margin-top: 1em;
  padding: 0.5em;
  background-color: #f0f0f0;
}

.related-terms ul {
  list-style-type: none;
  padding: 0;
}

.related-terms li {
  margin: 0.3em 0;
  padding: 0.2em 0.5em;
}

.wylie {
  font-family: monospace;
  font-size: 0.9em;
}

.unicode {
  font-family: 'DDC Uchen', 'Jomolhari', 'Qomolangma-Uchen Sarchung', Arial Unicode MS, Arial, sans-serif;
  font-size: 1.1em;
  text-rendering: optimizeLegibility;
}

.metadata {
  font-size: 0.85em;
  color: #999;
  margin-top: 1.5em;
  padding-top: 1em;
  border-top: 1px solid #ddd;
}

.author {
  font-size: 1.2em;
  font-style: italic;
  text-align: center;
  margin-top: 2em;
}

.timestamp {
  font-size: 0.9em;
  text-align: center;
  color: #999;
}

.description {
  text-align: center;
  font-style: italic;
  margin-bottom: 3em;
}
`

	_, err = io.WriteString(styleFile, style)
	return err
}

// formatTermChapter formats a single term chapter as XHTML
func (eg *EbookGenerator) formatTermChapter(chapterNum int, term TermData) string {
	// Display term: Unicode first, fallback to Wylie
	displayTerm := term.SearchTerm
	if displayTerm == "" {
		displayTerm = term.SearchTermWylie
	}

	// Build the title and content line - format exactly like related terms: Unicode (Wylie)
	var contentLine string
	if term.SearchTerm != "" || term.SearchTermWylie != "" {
		contentLine = fmt.Sprintf(`	<p><span class="unicode">%s</span> (<span class="wylie">%s</span>)</p>`, escapeXML(term.SearchTerm), escapeXML(term.SearchTermWylie))
	}

	chapter := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.1//EN" "http://www.w3.org/TR/xhtml11/DTD/xhtml11.dtd">
<html xmlns="http://www.w3.org/1999/xhtml">
	<head>
		<title>%s</title>
		<link rel="stylesheet" type="text/css" href="style.css"/>
	</head>
	<body>
	<h1><span class="unicode">%s</span> (<span class="wylie">%s</span>)</h1>
%s
`, escapeXML(displayTerm), escapeXML(term.SearchTerm), escapeXML(term.SearchTermWylie), contentLine)

	// Definitions
	if term.DefinitionsCount > 0 {
		chapter += "    <h2>Definitions</h2>\n"
		for dictName, def := range term.Definitions {
			if def != "" {
				formattedDef := formatDefinitionText(def)
				chapter += fmt.Sprintf(`    <div class="definition">
      <div class="dict-name">%s</div>
      <p>%s</p>
    </div>
`, escapeXML(dictName), escapeXML(formattedDef))
			}
		}
	}

	// Related terms
	if term.RelatedTermsCount > 0 {
		chapter += `    <div class="related-terms">
      <h2>Related Terms</h2>
      <ul>
`
		for _, rt := range term.RelatedTerms {
			if rt.Unicode != "" || rt.Wylie != "" {
				chapter += fmt.Sprintf(`        <li><span class="unicode">%s</span> (<span class="wylie">%s</span>)</li>
`, escapeXML(rt.Unicode), escapeXML(rt.Wylie))
			}
		}
		chapter += `      </ul>
    </div>
`
	}

	// Metadata footer
	chapter += fmt.Sprintf(`    <div class="metadata">
      <p>Term #%d | Definitions: %d | Related: %d</p>
    </div>
  </body>
</html>
`, chapterNum, term.DefinitionsCount, term.RelatedTermsCount)

	return chapter
}

// embedFont embeds the DDC Uchen Tibetan font in the EPUB
func (eg *EbookGenerator) embedFont(writer *zip.Writer) error {
	// Read the font file
	fontPath := filepath.Join(filepath.Dir(eg.inputDir), "DDC_Uchen-webfont.woff")
	fontData, err := ioutil.ReadFile(fontPath)
	if err != nil {
		// If font not found in expected location, try current directory
		fontPath = "DDC_Uchen-webfont.woff"
		fontData, err = ioutil.ReadFile(fontPath)
		if err != nil {
			// Non-fatal: font embedding failed but EPUB still valid without embedded font
			fmt.Fprintf(os.Stderr, "⚠️  Warning: Could not embed Tibetan font (not critical)\n")
			return nil
		}
	}

	// Create fonts directory in EPUB and add font file
	fontFile, err := writer.Create("OEBPS/fonts/DDC_Uchen-webfont.woff")
	if err != nil {
		return fmt.Errorf("failed to create font file in EPUB: %w", err)
	}

	_, err = fontFile.Write(fontData)
	return err
}

// escapeXML escapes special XML characters
func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

// formatDefinitionText cleans up and formats definition text
func formatDefinitionText(def string) string {
	// Fix common typos
	def = strings.ReplaceAll(def, "Abbrewiation", "Abbreviation")
	def = strings.ReplaceAll(def, "abbrewiation", "abbreviation")

	// Replace "for {TERM}" with just the Tibetan term (remove curly braces)
	def = strings.ReplaceAll(def, "{", "")
	def = strings.ReplaceAll(def, "}", "")

	// Clean up Tibetan diacritics mixed with Latin text
	// Remove combining Tibetan marks from after Latin characters
	re := regexp.MustCompile(`(\w+)་+`)
	def = re.ReplaceAllString(def, "$1 ")

	// Normalize multiple spaces
	for strings.Contains(def, "  ") {
		def = strings.ReplaceAll(def, "  ", " ")
	}

	return strings.TrimSpace(def)
}

// calculateJSONFilesSize calculates the total size of all JSON files in a directory
func calculateJSONFilesSize(dirPath string) int64 {
	var totalSize int64

	files, err := ioutil.ReadDir(dirPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  Warning: Could not read directory for size calculation: %v\n", err)
		return 0
	}

	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".json") {
			totalSize += file.Size()
		}
	}

	return totalSize
}

func main() {
	inputDir := flag.String("input", "./data", "Input directory containing JSON term files")
	paged := flag.Bool("paged", false, "Read per-page JSON files from the 'paged' subdirectory and treat each file as one ebook page")
	outputFile := flag.String("output", "tibetan-dictionary.epub", "Output EPUB/AZW file")
	title := flag.String("title", "Tibetan-English Dictionary", "Ebook title")
	author := flag.String("author", "Tibetan Dictionary Project", "Ebook author")
	flag.Parse()

	fmt.Println("📚 Tibetan Dictionary Ebook Generator")
	fmt.Println("=====================================")
	fmt.Printf("📁 Input directory: %s\n", *inputDir)
	fmt.Printf("📝 Output file base: %s\n", *outputFile)
	fmt.Printf("📏 Target ebook size: 29-32 MB per part\n")

	// Check if input directory exists
	if _, err := os.Stat(*inputDir); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Error: input directory not found: %s\n", *inputDir)
		os.Exit(1)
	}

	fmt.Println("⏳ Reading term files...")
	// If paged mode requested, read JSON files from the `paged` subdirectory
	inputPath := *inputDir
	if *paged {
		inputPath = filepath.Join(inputPath, "paged")
	}
	gen := NewEbookGenerator(inputPath, *outputFile, *title, *author)
	terms, err := gen.ReadTermFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Found %d terms\n", len(terms))

	// Calculate total size of JSON files to determine number of parts
	totalSize := calculateJSONFilesSize(inputPath)

	// Target size per ebook: 30MB (middle of 29-32MB range)
	const targetSize int64 = 30 * 1024 * 1024 // 30MB in bytes

	// Calculate number of parts needed
	numParts := int((totalSize + targetSize - 1) / targetSize) // Round up
	if numParts < 1 {
		numParts = 1
	}

	fmt.Printf("📊 Total JSON file size: %.2f MB\n", float64(totalSize)/(1024*1024))
	fmt.Printf("📊 Target size per ebook: 29-32 MB\n")
	fmt.Printf("📊 Generating %d ebook part(s)\n\n", numParts)

	// Split terms proportionally based on number of parts
	parts := make([][]TermData, numParts)
	termPerPart := (len(terms) + numParts - 1) / numParts // Round up

	for i := 0; i < numParts; i++ {
		startIdx := i * termPerPart
		endIdx := startIdx + termPerPart
		if endIdx > len(terms) {
			endIdx = len(terms)
		}
		if startIdx < len(terms) {
			parts[i] = terms[startIdx:endIdx]
		}
	}

	// Generate ebooks
	for i := 0; i < numParts; i++ {
		if len(parts[i]) == 0 {
			continue
		}

		// Remove .epub extension if present and add part number
		outputPath := *outputFile
		if strings.HasSuffix(outputPath, ".epub") {
			outputPath = strings.TrimSuffix(outputPath, ".epub")
		}
		if numParts == 1 {
			// If only one part, use the original filename
			outputPath = *outputFile
		} else {
			outputPath = fmt.Sprintf("%s-part-%d.epub", outputPath, i+1)
		}

		partTitle := *title
		if numParts > 1 {
			partTitle = fmt.Sprintf("%s - Part %d", *title, i+1)
		}

		gen := NewEbookGenerator(inputPath, outputPath, partTitle, *author)

		fmt.Printf("⏳ Generating Part %d EPUB ebook (%d terms)...\n", i+1, len(parts[i]))
		if err := gen.GenerateEPUB(parts[i]); err != nil {
			fmt.Fprintf(os.Stderr, "❌ Error generating EPUB part %d: %v\n", i+1, err)
			os.Exit(1)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
