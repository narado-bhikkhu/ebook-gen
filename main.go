package main

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

var workerCount = runtime.NumCPU()

// TermMeta is a compact metadata representation used in the first pass
type TermMeta struct {
	SearchTerm        string
	DefinitionsCount  int
	RelatedTermsCount int
}

// countingReader wraps an io.Reader and counts bytes read for progress reporting
type countingReader struct {
	r     io.Reader
	read  int64
	total int64
	start time.Time
	mu    sync.Mutex
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	cr.mu.Lock()
	if cr.start.IsZero() {
		cr.start = time.Now()
	}
	cr.read += int64(n)
	cr.mu.Unlock()
	return n, err
}

func (cr *countingReader) BytesRead() int64 {
	cr.mu.Lock()
	v := cr.read
	cr.mu.Unlock()
	return v
}

func (cr *countingReader) Percent() float64 {
	if cr.total <= 0 {
		return 0
	}
	cr.mu.Lock()
	r := cr.read
	cr.mu.Unlock()
	return float64(r) / float64(cr.total) * 100.0
}

func (cr *countingReader) BytesPerSec() float64 {
	cr.mu.Lock()
	read := cr.read
	start := cr.start
	cr.mu.Unlock()
	if start.IsZero() {
		return 0
	}
	elapsed := time.Since(start).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return float64(read) / elapsed
}

func (cr *countingReader) ETASeconds() float64 {
	if cr.total <= 0 {
		return 0
	}
	bps := cr.BytesPerSec()
	if bps <= 0 {
		return 0
	}
	remaining := float64(cr.total - cr.BytesRead())
	return remaining / bps
}

func humanDurationSecs(sec float64) string {
	if sec <= 0 {
		return "0s"
	}
	d := time.Duration(sec) * time.Second
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	} else if m > 0 {
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func humanBytesPerSec(bps float64) string {
	if bps <= 0 {
		return "0 B/s"
	}
	if bps >= 1024*1024 {
		return fmt.Sprintf("%.2f MB/s", bps/1024.0/1024.0)
	} else if bps >= 1024 {
		return fmt.Sprintf("%.2f KB/s", bps/1024.0)
	}
	return fmt.Sprintf("%.0f B/s", bps)
}

// TermData represents a single term's export from the Tibetan Dictionary CLI
type TermData struct {
	SearchTerm         string            `json:"searchTerm"`
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

// ReadTermFiles reads JSON term files. For very large aggregated files it returns an error and suggests streaming APIs
func (eg *EbookGenerator) ReadTermFiles() ([]TermData, string, error) {
	files, err := ioutil.ReadDir(eg.inputDir)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read directory %s: %w", eg.inputDir, err)
	}

	jsonFiles := []string{}
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".json") {
			jsonFiles = append(jsonFiles, filepath.Join(eg.inputDir, file.Name()))
		}
	}

	if len(jsonFiles) == 0 {
		return nil, "", fmt.Errorf("no JSON files found in %s", eg.inputDir)
	}

	// Quick heuristic: prefer per-term files (many small files) if there are multiple files
	if len(jsonFiles) > 1 {
		// Parse per-term files in parallel to utilize CPU and reduce wall time
		n := len(jsonFiles)
		numWorkers := workerCount
		if numWorkers <= 0 {
			numWorkers = 1
		}
		if numWorkers > n {
			numWorkers = n
		}
		inCh := make(chan string)
		type res struct {
			term TermData
			ok   bool
		}
		outCh := make(chan res)
		var wg sync.WaitGroup
		for i := 0; i < numWorkers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for path := range inCh {
					f, err := os.Open(path)
					if err != nil {
						continue
					}
					var term TermData
					dec := json.NewDecoder(f)
					if err := dec.Decode(&term); err == nil && term.SearchTerm != "" {
						outCh <- res{term: term, ok: true}
					}
					f.Close()
				}
			}()
		}

		go func() {
			for _, p := range jsonFiles {
				inCh <- p
			}
			close(inCh)
			wg.Wait()
			close(outCh)
		}()

		var terms []TermData
		processed := 0
		lastPrint := time.Now()
		start := time.Now()
		for r := range outCh {
			processed++
			if r.ok {
				terms = append(terms, r.term)
			}
			if time.Since(lastPrint) > 700*time.Millisecond {
				elapsed := time.Since(start).Seconds()
				filesPerSec := float64(processed) / (elapsed + 1e-9)
				etaSecs := float64(n-processed) / (filesPerSec + 1e-9)
				fmt.Printf("⏳ Parsing files: %d/%d | %.2f files/s | ETA %s\r", processed, n, filesPerSec, humanDurationSecs(etaSecs))
				lastPrint = time.Now()
			}
		}
		fmt.Printf("\n")
		if len(terms) == 0 {
			return nil, "", fmt.Errorf("no valid per-term JSON files found in %s", eg.inputDir)
		}
		sort.Slice(terms, func(i, j int) bool { return terms[i].SearchTerm < terms[j].SearchTerm })
		return terms, "", nil
	}

	// Single JSON file: treat as aggregated and return its path for streaming
	return nil, jsonFiles[0], nil
}

// CollectAggregatedMetadata scans a large aggregated JSON file and builds a lightweight list of TermMeta
func (eg *EbookGenerator) CollectAggregatedMetadata(path string, progressInterval time.Duration) ([]TermMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open aggregated file: %w", err)
	}
	defer f.Close()

	fi, _ := f.Stat()
	cr := &countingReader{r: bufio.NewReader(f), total: fi.Size(), start: time.Now()}
	dec := json.NewDecoder(cr)

	// Use token streaming to find "terms" object
	// Start progress printer
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(progressInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				pct := cr.Percent()
				bps := cr.BytesPerSec()
				eta := cr.ETASeconds()
				fmt.Printf("⏳ Generating EPUB (metadata): %.2f%% | %s | ETA %s\r", pct, humanBytesPerSec(bps), humanDurationSecs(eta))
			case <-stop:
				return
			}
		}
	}()

	var metas []TermMeta
	// Walk the JSON tokens until we find the "terms" key
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			close(stop)
			return nil, fmt.Errorf("json token error: %w", err)
		}
		if key, ok := tok.(string); ok && key == "terms" {
			// Expect start of object
			if _, err := dec.Token(); err != nil { // should be Delim '{'
				close(stop)
				return nil, fmt.Errorf("malformed aggregated JSON: %w", err)
			}
			// Iterate entries
			for dec.More() {
				// read key
				kTok, err := dec.Token()
				if err != nil {
					close(stop)
					return nil, fmt.Errorf("token error: %w", err)
				}
				keyStr := ""
				if s, ok := kTok.(string); ok {
					keyStr = s
				}

				// Decode the value into a small map to extract counts and searchTerm.
				var small struct {
					SearchTerm        string `json:"searchTerm"`
					DefinitionsCount  int    `json:"definitionsCount"`
					RelatedTermsCount int    `json:"relatedTermsCount"`
				}
				if err := dec.Decode(&small); err != nil {
					// on error, try to skip the value gracefully
					return nil, fmt.Errorf("decode term %s: %w", keyStr, err)
				}
				if small.SearchTerm == "" {
					small.SearchTerm = keyStr
				}
				metas = append(metas, TermMeta{SearchTerm: small.SearchTerm, DefinitionsCount: small.DefinitionsCount, RelatedTermsCount: small.RelatedTermsCount})
			}
			close(stop)
			break
		}
	}

	// sort metas
	sort.Slice(metas, func(i, j int) bool { return metas[i].SearchTerm < metas[j].SearchTerm })
	return metas, nil
}

// StreamAggregatedTerms streams terms from a large aggregated JSON file and calls fn for each TermData in the same order as metadata
func (eg *EbookGenerator) StreamAggregatedTerms(path string, progressInterval time.Duration, fn func(int, TermData) error) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open aggregated file: %w", err)
	}
	defer f.Close()

	fi, _ := f.Stat()
	cr := &countingReader{r: bufio.NewReader(f), total: fi.Size(), start: time.Now()}
	dec := json.NewDecoder(cr)

	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(progressInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				pct := cr.Percent()
				bps := cr.BytesPerSec()
				eta := cr.ETASeconds()
				fmt.Printf("⏳ Generating EPUB (writing): %.2f%% | %s | ETA %s\r", pct, humanBytesPerSec(bps), humanDurationSecs(eta))
			case <-stop:
				return
			}
		}
	}()

	idx := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			close(stop)
			return fmt.Errorf("json token error: %w", err)
		}
		if key, ok := tok.(string); ok && key == "terms" {
			if _, err := dec.Token(); err != nil {
				close(stop)
				return fmt.Errorf("malformed aggregated JSON: %w", err)
			}
			for dec.More() {
				// read key
				kTok, err := dec.Token()
				if err != nil {
					close(stop)
					return fmt.Errorf("token error: %w", err)
				}
				keyStr := ""
				if s, ok := kTok.(string); ok {
					keyStr = s
				}

				// decode flexibly into a map and construct TermData permissively
				var rawTerm map[string]interface{}
				if err := dec.Decode(&rawTerm); err != nil {
					close(stop)
					return fmt.Errorf("decode term at index %d: %w", idx, err)
				}
				var t TermData
				// searchTerm
				if v, ok := rawTerm["searchTerm"].(string); ok && v != "" {
					t.SearchTerm = v
				} else {
					t.SearchTerm = keyStr
				}

				// definitions
				t.Definitions = map[string]string{}
				if defs, ok := rawTerm["definitions"].(map[string]interface{}); ok {
					for k, vv := range defs {
						t.Definitions[k] = fmt.Sprintf("%v", vv)
					}
				} else if s, ok := rawTerm["definitions"].(string); ok {
					t.Definitions["default"] = s
				}

				// definitionsWylie
				t.DefinitionsWylie = map[string]string{}
				if defs, ok := rawTerm["definitionsWylie"].(map[string]interface{}); ok {
					for k, vv := range defs {
						t.DefinitionsWylie[k] = fmt.Sprintf("%v", vv)
					}
				} else if s, ok := rawTerm["definitionsWylie"].(string); ok {
					t.DefinitionsWylie["default"] = s
				}

				// definitionsUnicode
				t.DefinitionsUnicode = map[string]string{}
				if defs, ok := rawTerm["definitionsUnicode"].(map[string]interface{}); ok {
					for k, vv := range defs {
						t.DefinitionsUnicode[k] = fmt.Sprintf("%v", vv)
					}
				} else if s, ok := rawTerm["definitionsUnicode"].(string); ok {
					t.DefinitionsUnicode["default"] = s
				}

				// relatedTerms
				t.RelatedTerms = []RelatedTerm{}
				if rts, ok := rawTerm["relatedTerms"].([]interface{}); ok {
					for _, ri := range rts {
						if rmap, ok := ri.(map[string]interface{}); ok {
							rt := RelatedTerm{}
							if w, ok := rmap["wylie"].(string); ok {
								rt.Wylie = w
							}
							if u, ok := rmap["unicode"].(string); ok {
								rt.Unicode = u
							}
							t.RelatedTerms = append(t.RelatedTerms, rt)
						}
					}
				}

				// counts
				if dc, ok := rawTerm["definitionsCount"].(float64); ok {
					t.DefinitionsCount = int(dc)
				} else if dcI, ok := rawTerm["definitionsCount"].(int); ok {
					t.DefinitionsCount = dcI
				}
				if rc, ok := rawTerm["relatedTermsCount"].(float64); ok {
					t.RelatedTermsCount = int(rc)
				} else if rcI, ok := rawTerm["relatedTermsCount"].(int); ok {
					t.RelatedTermsCount = rcI
				}

				if err := fn(idx, t); err != nil {
					close(stop)
					return err
				}
				idx++
			}
			close(stop)
			break
		}
	}

	return nil
}

// GenerateEPUB generates an EPUB file from the term data
// GenerateEPUB takes either an in-memory terms slice (small inputs) or streams terms from an aggregated file.
// If aggregatedPath is non-empty, it will stream terms from that file instead of using the in-memory slice.
func (eg *EbookGenerator) GenerateEPUB(terms []TermData, aggregatedPath string, progressInterval time.Duration) error {
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

	// If streaming from aggregated path, we need metadata first
	var metas []TermMeta
	if aggregatedPath != "" {
		fmt.Println("⏳ Building metadata from aggregated file (pass 1)...")
		m, err := eg.CollectAggregatedMetadata(aggregatedPath, progressInterval)
		if err != nil {
			return err
		}
		metas = m
		fmt.Printf("✓ Found %d terms (metadata)\n", len(metas))
	} else {
		// Build simple metas from terms slice
		for _, t := range terms {
			metas = append(metas, TermMeta{SearchTerm: t.SearchTerm, DefinitionsCount: t.DefinitionsCount, RelatedTermsCount: t.RelatedTermsCount})
		}
	}

	// Write container, opf, toc, title (we can use metas to write a compact TOC)
	if err := eg.writeContainerXML(writer); err != nil {
		return err
	}
	fmt.Printf("⏳ Generating EPUB: writing manifest and TOC for %d terms...\n", len(metas))
	if err := eg.writeContentOPF_FromMetas(writer, metas); err != nil {
		return err
	}
	if err := eg.writeTOC_FromMetas(writer, metas); err != nil {
		return err
	}
	if err := eg.writeTitlePage(writer); err != nil {
		return err
	}

	// Now write chapters. If aggregated path provided, stream and write incrementally.
	fmt.Println("⏳ Writing term chapters...")
	if aggregatedPath != "" {
		termIdx := 0
		start := time.Now()
		err := eg.StreamAggregatedTerms(aggregatedPath, progressInterval, func(idx int, term TermData) error {
			// create chapter file in zip
			chapterFile, err := writer.Create(fmt.Sprintf("OEBPS/chapter%d.xhtml", idx+1))
			if err != nil {
				return err
			}
			chapter := eg.formatTermChapter(idx+1, term)
			if _, err := io.WriteString(chapterFile, chapter); err != nil {
				return err
			}
			termIdx = idx + 1
			if termIdx%100 == 0 {
				elapsed := time.Since(start).Seconds()
				chapPerSec := float64(termIdx) / (elapsed + 1e-9)
				etaSecs := float64(len(metas)-termIdx) / (chapPerSec + 1e-9)
				fmt.Printf("   ✓ Written %d chapters | %.2f ch/s | ETA %s\r", termIdx, chapPerSec, humanDurationSecs(etaSecs))
			}
			return nil
		})
		if err != nil {
			return err
		}
		fmt.Printf("\n✓ Written %d chapters\n", termIdx)
	} else {
		// In-memory path: iterate terms slice and write
		start := time.Now()
		for i, term := range terms {
			chapterFile, err := writer.Create(fmt.Sprintf("OEBPS/chapter%d.xhtml", i+1))
			if err != nil {
				return err
			}
			chapter := eg.formatTermChapter(i+1, term)
			if _, err := io.WriteString(chapterFile, chapter); err != nil {
				return err
			}
			if (i+1)%100 == 0 {
				elapsed := time.Since(start).Seconds()
				chapPerSec := float64(i+1) / (elapsed + 1e-9)
				etaSecs := float64(len(terms)-(i+1)) / (chapPerSec + 1e-9)
				fmt.Printf("   ✓ Written %d chapters | %.2f ch/s | ETA %s\r", i+1, chapPerSec, humanDurationSecs(etaSecs))
			}
		}
		fmt.Printf("\n✓ Written %d chapters\n", len(terms))
	}

	// Write style and font
	if err := eg.embedFont(writer); err != nil {
		// warn, but continue
		fmt.Fprintf(os.Stderr, "⚠️  Warning embedding font: %v\n", err)
	}

	// write stylesheet
	if err := eg.writeStyle(writer); err != nil {
		return err
	}

	fmt.Printf("✅ EPUB ebook created: %s\n", eg.outputFile)
	fmt.Printf("📖 Contains %d terms\n", len(metas))
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

// writeContentOPF_FromMetas writes the OEBPS/content.opf (package) file using light metadata
// This uses incremental writes instead of building a huge string to avoid O(n^2) behavior for large term counts
func (eg *EbookGenerator) writeContentOPF_FromMetas(writer *zip.Writer, metas []TermMeta) error {
	f, err := writer.Create("OEBPS/content.opf")
	if err != nil {
		return err
	}

	// Header
	_, err = io.WriteString(f, fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
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
    <item id="title" href="title.xhtml" media-type="application/xhtml+xml"/>
`, eg.title, eg.author, time.Now().Format("2006-01-02"), time.Now().Unix()))
	if err != nil {
		return err
	}

	// Manifest items (streaming write with periodic progress)
	manifestProgress := 5000
	for i := range metas {
		if (i+1)%manifestProgress == 0 {
			fmt.Printf("⏳ Generating EPUB: manifest %d/%d\r", i+1, len(metas))
		}
		if _, err := io.WriteString(f, fmt.Sprintf("    <item id=\"chapter%d\" href=\"chapter%d.xhtml\" media-type=\"application/xhtml+xml\"/>\n", i+1, i+1)); err != nil {
			return err
		}
	}
	fmt.Printf("\n")

	// Spine header
	if _, err := io.WriteString(f, `  </manifest>
  <spine toc="ncx">
    <itemref idref="title"/>
`); err != nil {
		return err
	}

	// Spine items (progress shown intermittently)
	spineProgress := 5000
	for i := range metas {
		if (i+1)%spineProgress == 0 {
			fmt.Printf("⏳ Generating EPUB: spine %d/%d\r", i+1, len(metas))
		}
		if _, err := io.WriteString(f, fmt.Sprintf("    <itemref idref=\"chapter%d\"/>\n", i+1)); err != nil {
			return err
		}
	}
	fmt.Printf("\n")

	// Footer
	if _, err := io.WriteString(f, `  </spine>
  <guide>
    <reference type="toc" title="Table of Contents" href="toc.xhtml"/>
    <reference type="cover" title="Cover" href="title.xhtml"/>
  </guide>
</package>`); err != nil {
		return err
	}

	return nil
}

// writeTOC_FromMetas writes the OEBPS/toc.ncx file using light metadata
// Use incremental writes to avoid huge temporary allocations for very large TOCs
func (eg *EbookGenerator) writeTOC_FromMetas(writer *zip.Writer, metas []TermMeta) error {
	f, err := writer.Create("OEBPS/toc.ncx")
	if err != nil {
		return err
	}

	if _, err := io.WriteString(f, `<?xml version="1.0" encoding="UTF-8"?>
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
`); err != nil {
		return err
	}

	// Write navPoints with periodic progress updates
	tocProgress := 5000
	for i, meta := range metas {
		if (i+1)%tocProgress == 0 {
			fmt.Printf("⏳ Generating EPUB: writing TOC %d/%d\r", i+1, len(metas))
		}
		if _, err := io.WriteString(f, fmt.Sprintf("    <navPoint id=\"chapter%d\" playOrder=\"%d\">\n", i+1, i+2)); err != nil {
			return err
		}
		if _, err := io.WriteString(f, fmt.Sprintf("      <navLabel><text>%s</text></navLabel>\n", escapeXML(meta.SearchTerm))); err != nil {
			return err
		}
		if _, err := io.WriteString(f, fmt.Sprintf("      <content src=\"chapter%d.xhtml\"/>\n", i+1)); err != nil {
			return err
		}
		if _, err := io.WriteString(f, "    </navPoint>\n"); err != nil {
			return err
		}
	}
	fmt.Printf("\n")

	if _, err := io.WriteString(f, `  </navMap>
</ncx>`); err != nil {
		return err
	}

	return nil
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

	// Use shared helper to write stylesheet
	if err := eg.writeStyle(writer); err != nil {
		return err
	}
	return nil
}

// writeStyle writes the full stylesheet used by the EPUB
func (eg *EbookGenerator) writeStyle(writer *zip.Writer) error {
	styleFile, err := writer.Create("OEBPS/style.css")
	if err != nil {
		return err
	}

	style := `
@font-face {
  font-family: 'DDC Uchen';
  src: url('fonts/DDC_Uchen-webfont.woff') format('woff');
  font-weight: normal;
  font-style: normal;
}

html, body {
  padding: 0;
  margin: 0;
}

body {
  color: #000;
  background-color: #fff;
  font-family: "Droid Sans", "DejaVu Sans", LiberationSans, Arial, sans-serif;
  line-height: 1.6;
  margin: 1em;
  text-rendering: optimizeLegibility;
}

h1 {
  font-family: "Droid Sans", "DejaVu Sans", LiberationSans, Arial, sans-serif;
  font-size: 1.3em;
  margin-top: 0.5em;
  margin-bottom: 0.3em;
  color: #333;
  font-weight: normal;
}

h1.definitionHead {
  font-size: 2em;
  font-weight: normal;
}

h2 {
  font-family: "Droid Sans", "DejaVu Sans", LiberationSans, Arial, sans-serif;
  font-size: 1.3em;
  margin-top: 0.8em;
  margin-bottom: 0.3em;
  color: #555;
  border-bottom: 1px solid #ddd;
  padding-bottom: 0.2em;
}

.definition {
  margin-left: 0;
  margin-bottom: 0.5em;
  padding: 0.3em 0.3em 0.3em 0.7em;
  background-color: transparent;
  border-left: none;
  font-size: 1.2em;
  font-family: "Droid Serif", "DejaVu Serif", Verdana, Georgia, serif;
}

.definition p {
  margin: 0 0 0.45em 0;
}

.dict-name {
  color: #666;
  font-weight: bold;
  font-family: "Droid Sans", "DejaVu Sans", LiberationSans, Arial, sans-serif;
  padding: 0.6em 0.9em 0.6em 0.3em;
  font-size: 1em;
}

.related-terms {
  margin-top: 1em;
  padding: 0.5em;
  background-color: #f9f9f9;
  border-left: 3px solid #ddd;
}

.related-terms h2 {
  margin-top: 0;
}

 .related-terms ul {
  list-style-type: disc;
  padding-left: 1.2em;
  margin: 0.5em 0;
 }

.related-terms li {
  margin: 0.3em 0;
  padding: 0.2em 0;
  font-size: 1.1em;
}

.wylie {
  font-family: monospace;
  font-size: 0.9em;
}

.tib {
  font-family: "Droid Sans", "Jomolhari", "Jomolhari ID", "DDC Uchen", "Kailasa", "DDC Rinzin", "Uchen_05", "Qomolangma-Uchen Sarchung", "Qomolangma-Uchen Sutung", "Narthang", "CTRC-Uchen", "Monlam Uni OuChan2", "Monlam Uni OuChan1", "XenoType Tibetan New", "TCRC Youtso Unicode", "Tibetan Machine Uni", "DDCRinzin-webfont", "SambhotaDege", "Microsoft Himalaya", "Tib-US Unicode";
  font-size: 1.4em;
  line-height: 180%;
  text-rendering: optimizeLegibility;
}

.unicode {
  font-family: "Droid Sans", "Jomolhari", "Jomolhari ID", "DDC Uchen", "Kailasa", "DDC Rinzin", "Uchen_05", "Qomolangma-Uchen Sarchung", "Qomolangma-Uchen Sutung", "Narthang", "CTRC-Uchen", "Monlam Uni OuChan2", "Monlam Uni OuChan1", "XenoType Tibetan New", "TCRC Youtso Unicode", "Tibetan Machine Uni", "DDCRinzin-webfont", "SambhotaDege", "Microsoft Himalaya", "Tib-US Unicode";
  font-size: 1.4em;
  line-height: 180%;
  text-rendering: optimizeLegibility;
}

.inlineTib {
  font-family: "Droid Sans", "Jomolhari", "Jomolhari ID", "DDC Uchen", "Kailasa", "DDC Rinzin", "Uchen_05", "Qomolangma-Uchen Sarchung", "Qomolangma-Uchen Sutung", "Narthang", "CTRC-Uchen", "Monlam Uni OuChan2", "Monlam Uni OuChan1", "XenoType Tibetan New", "TCRC Youtso Unicode", "Tibetan Machine Uni", "DDCRinzin-webfont", "SambhotaDege", "Microsoft Himalaya", "Tib-US Unicode";
  font-size: 1.4em;
  line-height: 170%;
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
  font-family: "Droid Sans", "DejaVu Sans", LiberationSans, Arial, sans-serif;
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
  font-family: "Droid Sans", "DejaVu Sans", LiberationSans, Arial, sans-serif;
}

a {
  color: #222;
  text-decoration: none;
}

a:hover {
  text-decoration: underline;
}
`

	_, err = io.WriteString(styleFile, style)
	return err
}

// formatTermChapter formats a single term chapter as XHTML
func (eg *EbookGenerator) formatTermChapter(chapterNum int, term TermData) string {
	// Determine best Unicode representation for the search term
	termUnicode := ""
	// Prefer DefinitionsUnicode values if present
	if term.DefinitionsUnicode != nil {
		for _, v := range term.DefinitionsUnicode {
			if strings.TrimSpace(v) != "" {
				termUnicode = v
				break
			}
		}
	}
	// Fallback: check related terms for a self-match
	if termUnicode == "" {
		for _, rt := range term.RelatedTerms {
			if rt.Wylie == term.SearchTerm && strings.TrimSpace(rt.Unicode) != "" {
				termUnicode = rt.Unicode
				break
			}
		}
	}

	chapter := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.1//EN" "http://www.w3.org/TR/xhtml11/DTD/xhtml11.dtd">
<html xmlns="http://www.w3.org/1999/xhtml">
  <head>
    <title>%s</title>
    <link rel="stylesheet" type="text/css" href="style.css"/>
  </head>
  <body>
	<h1>%s</h1>
	<p>
	  `,
		escapeXML(term.SearchTerm))

	// Show Unicode if we found one; otherwise show search term as-is in Unicode slot
	if termUnicode != "" {
		chapter += fmt.Sprintf("    <span class=\"unicode\">%s</span> (<span class=\"wylie\">%s</span>)\n", escapeXML(termUnicode), escapeXML(term.SearchTerm))
	} else {
		chapter += fmt.Sprintf("    <span class=\"wylie\">%s</span>\n", escapeXML(term.SearchTerm))
	}

	// close header paragraph
	chapter += "    </p>\n"

	// Definitions: show Unicode and Wylie forms when available alongside the definition text
	if term.DefinitionsCount > 0 {
		chapter += "    <h2>Definitions</h2>\n"

		// Collect union of dict names
		dictNames := map[string]struct{}{}
		for k := range term.Definitions {
			dictNames[k] = struct{}{}
		}
		for k := range term.DefinitionsWylie {
			dictNames[k] = struct{}{}
		}
		for k := range term.DefinitionsUnicode {
			dictNames[k] = struct{}{}
		}

		// Iterate consistently (sorted) for stable output
		names := []string{}
		for k := range dictNames {
			names = append(names, k)
		}
		sort.Strings(names)

		for _, dictName := range names {
			def := term.Definitions[dictName]
			wdef := term.DefinitionsWylie[dictName]
			udef := term.DefinitionsUnicode[dictName]

			chapter += fmt.Sprintf("    <div class=\"definition\">\n      <div class=\"dict-name\">%s</div>\n", escapeXML(dictName))

			if strings.TrimSpace(udef) != "" {
				chapter += fmt.Sprintf("      <p><span class=\"unicode\">%s</span></p>\n", escapeXML(udef))
			}
			if strings.TrimSpace(wdef) != "" {
				chapter += fmt.Sprintf("      <p><span class=\"wylie\">%s</span></p>\n", escapeXML(wdef))
			}
			if strings.TrimSpace(def) != "" {
				chapter += fmt.Sprintf("      <p>%s</p>\n", escapeXML(def))
			}

			chapter += "    </div>\n"
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

func main() {
	inputDir := flag.String("input", "/workspaces/codespaces-blank/ebook/data", "Input directory containing JSON term files")
	outputFile := flag.String("output", "tibetan-dictionary.epub", "Output EPUB/AZW file")
	title := flag.String("title", "Tibetan-English Dictionary", "Ebook title")
	author := flag.String("author", "Tibetan Dictionary Project", "Ebook author")
	workers := flag.Int("workers", workerCount, "Number of concurrent workers when parsing many small JSON files")
	progressInterval := flag.Int("progress-interval", 700, "Progress refresh interval in ms")
	flag.Parse()
	workerCount = *workers
	progIntervalDur := time.Duration(*progressInterval) * time.Millisecond

	fmt.Println("📚 Tibetan Dictionary Ebook Generator")
	fmt.Println("=====================================")
	fmt.Printf("📁 Input directory: %s\n", *inputDir)
	fmt.Printf("📝 Output file: %s\n", *outputFile)

	// Check if input directory exists
	if _, err := os.Stat(*inputDir); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Error: input directory not found: %s\n", *inputDir)
		os.Exit(1)
	}

	fmt.Println("⏳ Reading term files (detecting large aggregated inputs)...")
	gen := NewEbookGenerator(*inputDir, *outputFile, *title, *author)
	terms, aggPath, err := gen.ReadTermFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Error: %v\n", err)
		os.Exit(1)
	}

	if aggPath != "" {
		fmt.Printf("✓ Detected large aggregated JSON: %s\n", aggPath)
		fmt.Println("⏳ Generating EPUB from aggregated input (streaming)...")
		// Initial progress line so user immediately sees the progress UI
		fmt.Printf("⏳ Generating EPUB: 0.00%% | 0 B/s | ETA --\r")
		if err := gen.GenerateEPUB(nil, aggPath, progIntervalDur); err != nil {
			fmt.Fprintf(os.Stderr, "❌ Error generating EPUB: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Printf("✓ Found %d terms\n\n", len(terms))
		fmt.Println("⏳ Generating EPUB ebook...")
		if err := gen.GenerateEPUB(terms, "", progIntervalDur); err != nil {
			fmt.Fprintf(os.Stderr, "❌ Error generating EPUB: %v\n", err)
			os.Exit(1)
		}
	}
}
