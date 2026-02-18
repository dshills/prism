package output

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/dshills/prism/internal/review"
)

// JSONWriter outputs the full report as JSON.
type JSONWriter struct{}

func (j *JSONWriter) Write(w io.Writer, report *review.Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling JSON: %w", err)
	}
	_, err = w.Write(data)
	if err != nil {
		return fmt.Errorf("writing JSON: %w", err)
	}
	_, err = fmt.Fprintln(w)
	return err
}
