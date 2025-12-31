package samplegen

import (
	"encoding/json"
	"fmt"
	"os"
)

// GenerateSample writes a sample aggregated JSON to stdout (used for local testing only)
func GenerateSample() {
	d := map[string]interface{}{
		"timestamp":  "2025-12-31",
		"totalTerms": 2000,
		"terms":      map[string]interface{}{},
	}
	t := d["terms"].(map[string]interface{})
	for i := 0; i < 2000; i++ {
		k := fmt.Sprintf("term_%04d", i)
		t[k] = map[string]interface{}{
			"searchTerm":         k,
			"definitionsCount":   1,
			"relatedTermsCount":  2,
			"definitions":        map[string]string{"dict1": "definition of " + k},
			"definitionsUnicode": map[string]string{"dict1": "unicode " + k},
			"definitionsWylie":   map[string]string{"dict1": "wylie " + k},
			"relatedTerms":       []map[string]string{{"wylie": "rt1_" + k, "unicode": "rtu1_" + k}, {"wylie": "rt2_" + k, "unicode": "rtu2_" + k}},
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(d)
}
