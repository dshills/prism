package output

import (
	"fmt"
	"io"
	"os"

	"github.com/dshills/prism/internal/review"
)

// Writer writes a report in a specific format.
type Writer interface {
	Write(w io.Writer, report *review.Report) error
}

// GetWriter returns a writer for the specified format.
func GetWriter(format string) (Writer, error) {
	switch format {
	case "text":
		return &TextWriter{}, nil
	case "json":
		return &JSONWriter{}, nil
	default:
		return nil, fmt.Errorf("unsupported output format: %s", format)
	}
}

// WriteReport writes the report to the specified output (file path or stdout).
func WriteReport(report *review.Report, format, outPath string) error {
	writer, err := GetWriter(format)
	if err != nil {
		return err
	}

	var w io.Writer
	if outPath != "" {
		f, err := os.Create(outPath)
		if err != nil {
			return fmt.Errorf("creating output file: %w", err)
		}
		defer f.Close()
		w = f
	} else {
		w = os.Stdout
	}

	return writer.Write(w, report)
}
