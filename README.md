# Tibetan Dictionary Ebook Generator

A Go application that converts Tibetan Dictionary JSON exports into EPUB ebooks (which can be converted to AZW/Kindle format).

## 📋 Overview

This tool reads JSON term files exported from the Tibetan Dictionary CLI (`--export-all-terms`) and generates a beautiful, navigable EPUB ebook with:

- **Title page** with metadata
- **Chapters** for each term (sorted alphabetically)
- **Definitions** from multiple dictionary sources (Wylie and Unicode)
- **Related terms** with cross-references
- **Professional styling** optimized for reading

## 🚀 Quick Start

### Prerequisites

- Go 1.21 or later
- JSON term files in `/workspaces/codespaces-blank/ebook/data/` (from CLI export)

### Build

```bash
cd /workspaces/codespaces-blank/ebook
go build -o ebook-gen main.go
```

### Run

```bash
# Default: read from ./data, output to tibetan-dictionary.epub
./ebook-gen

# Or with custom options
./ebook-gen \
  -input /workspaces/books/ebook/data \
  -output tibetan-english-dict.epub \
  -title "Tibetan English Dictionary" \
  -author "Narado"
```

## 📖 Usage Examples

### Generate from default location

```bash
./ebook-gen
```

### Generate with custom title and author

```bash
./ebook-gen \
  -title "Buddhist Dictionary" \
  -author "Tenzin Dorje"
```

### Generate from a specific export directory

```bash
./ebook-gen \
  -input /workspaces/codespaces-blank/dict-data/all-terms/per-term \
  -output buddhist-terms.epub
```

## 🔧 Command-Line Options

```
-input string
    Input directory containing JSON term files
    (default: /workspaces/codespaces-blank/ebook/data)

-output string
    Output EPUB file path
    (default: tibetan-dictionary.epub)

-title string
    Ebook title
    (default: Tibetan-English Dictionary)

-author string
    Ebook author
    (default: Tibetan Dictionary Project)

-workers int
    Number of concurrent workers when parsing many small JSON files (default: number of CPU cores)
```

## 📦 Output Format

The generator creates a valid EPUB 2.0 file (ZIP archive with XML/HTML content) containing:

```
├── mimetype                      # EPUB mimetype declaration
├── META-INF/
│   └── container.xml             # Package metadata
├── OEBPS/
│   ├── content.opf               # Package document (manifest & spine)
│   ├── toc.ncx                   # Table of contents
│   ├── title.xhtml               # Title page
│   ├── chapter1.xhtml            # Term chapter 1
│   ├── chapter2.xhtml            # Term chapter 2
│   ├── ...
│   └── style.css                 # Styling
```

Each term chapter includes:
- **Term** in Tibetan Unicode and Wylie
- **Definitions** from each dictionary source (original, Wylie, Unicode)
- **Related terms** with both forms
- **Metadata** (chapter number, counts)

## 🔄 Converting EPUB to AZW/Kindle Format

### Option 1: Using Calibre (recommended)

```bash
# Install Calibre (macOS)
brew install calibre

# Convert EPUB to AZW3 (modern Kindle format)
ebook-convert tibetan-dictionary.epub tibetan-dictionary.azw3

# Convert to older AZW format
ebook-convert tibetan-dictionary.epub tibetan-dictionary.azw
```

### Option 2: Using Amazon's KindleGen

```bash
# Download KindleGen from Amazon
# https://www.amazon.com/gp/feature.html?docId=1000765211

kindlegen tibetan-dictionary.epub -o tibetan-dictionary.mobi
```

### Option 3: Upload to Kindle Cloud Reader

1. Generate the EPUB file
2. Email it to your Kindle email address (found in Amazon account settings)
3. It will automatically appear in your Kindle library

## 📝 JSON Input Format

The generator expects JSON files matching the format from the Tibetan Dictionary CLI `--export-all-terms` command:

```json
{
  "searchTerm": "'am",
  "timestamp": "2025-12-31T15:33:39.513Z",
  "definitions": {
    "Hopkins 2015": "or; and; question marker; or else"
  },
  "definitionsWylie": {
    "Hopkins 2015": "or; and; question marker; or else"
  },
  "definitionsUnicode": {
    "Hopkins 2015": "འམ་"
  },
  "relatedTerms": [
    {"wylie": "'am ling", "unicode": "འམ་གླིང་"},
    {"wylie": "'am bya", "unicode": "འམ་བྱ་"}
  ],
  "definitionsCount": 1,
  "relatedTermsCount": 2
}
```

## 🎨 Styling

The generated ebook includes professional CSS styling optimized for:
- **Readability**: Georgia serif font, comfortable line spacing
- **Navigation**: Clear section headers and chapter divisions
- **Tibetan Unicode**: Support for proper Tibetan character rendering using the DDC Uchen font
- **Kindle devices**: Compatible with all Kindle models
- **Font rendering**: Optimized with CSS text-rendering for crisp glyphs

### 🔤 Tibetan Font Features

The EPUB generator automatically:
1. **Embeds the DDC Uchen Tibetan font** in the EPUB file (339KB WOFF format)
2. **Applies proper font-family stack** for Tibetan text:
   - Primary: DDC Uchen (embedded)
   - Fallback: Jomolhari, Qomolangma-Uchen Sarchung (if system has)
   - Final fallback: Arial Unicode MS, Arial, sans-serif

3. **Uses text-rendering optimization** for crisp, legible Tibetan glyphs

This ensures Tibetan Unicode displays consistently across:
- Kindle devices (all models)
- EPUB readers (desktop & mobile)
- Web browsers (if converted to HTML)

Example EPUB file size: ~353KB (with 25 terms + embedded font)

You can customize styling by editing the CSS in the source code.

## ⚙️ Features

- ✅ Reads multiple JSON files from a directory
- ✅ Sorts terms alphabetically
- ✅ Generates valid EPUB 2.0 format
- ✅ Includes both Wylie and Unicode forms
- ✅ Professional HTML/CSS styling
- ✅ Table of contents for navigation
- ✅ Scalable to thousands of terms
- ✅ Can be converted to AZW/Kindle format

## 🚀 Performance

- Tested with 100+ terms
- Processing speed: ~100-1000 terms per second (varies by CPU and I/O)
- Output file size: ~0.5-2 MB per 1000 terms (depends on definition length)

**Large export support & progress reporting** ✅

- The generator now detects a single large aggregated JSON file and processes it in a streaming two-pass mode to avoid loading the whole file into memory (ideal for 600–700MB exports).
- During streaming the CLI shows live progress (percent of bytes processed) while building metadata and while writing chapters, plus periodic chapter counts.
- Per-term formats (many small JSON files) are parsed with a small worker pool to speed up I/O-bound workloads and show parsing progress.

## 📄 License

Same as the main Tibetan Dictionary project.

## 🤝 Integration with CLI

To generate an ebook from the Tibetan Dictionary CLI:

```bash
# 1. Export a subset of terms as JSON
cd /workspaces/codespaces-blank/tibetan-english-cli
node dict-cli.js --export-all-terms \
  --export-mode per-term \
  --limit 1000 \
  --out-dir /workspaces/codespaces-blank/ebook/data

# 2. Generate the EPUB ebook
cd /workspaces/codespaces-blank/ebook
go build -o ebook-gen main.go
./ebook-gen -title "My Tibetan Dictionary"

# 3. Convert to AZW3 (if Calibre is installed)
ebook-convert tibetan-dictionary.epub tibetan-dictionary.azw3
```

## 🐛 Troubleshooting

**"no valid JSON term files found"**
- Ensure JSON files are in the input directory
- Check that files end with `.json`
- Verify files contain valid `"searchTerm"` field

**EPUB won't open in reader**
- The file might be corrupted; try regenerating
- Ensure Go version is 1.21+
- Check that input JSON is valid

**Kindle conversion fails**
- EPUB files are valid; conversion tool may have issues
- Try with Calibre first (more robust)
- Check file encoding (should be UTF-8)

## 📚 Resources

- [EPUB Specification](https://idpf.org/epub/20/)
- [Calibre Documentation](https://calibre-ebook.com/)
- [Amazon Kindle Publishing](https://kdp.amazon.com/)
