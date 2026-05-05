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
	case "markdown", "md":
		return &MarkdownWriter{}, nil
	case "sarif":
		return &SARIFWriter{}, nil
	default:
		return nil, fmt.Errorf("unsupported output format: %s", format)
	}
}

// WriteReport writes the report to the specified output (file path or stdout).
func WriteReport(report *review.Report, format, outPath string) (err error) {
	var writer Writer
	writer, err = GetWriter(format)
	if err != nil {
		return
	}

	var w io.Writer
	if outPath != "" {
		var f *os.File
		f, err = os.Create(outPath)
		if err != nil {
			return fmt.Errorf("creating output file: %w", err)
		}
		defer func() {
			if cerr := f.Close(); cerr != nil && err == nil {
				err = fmt.Errorf("closing output file: %w", cerr)
			}
		}()
		w = f
	} else {
		w = os.Stdout
	}

	err = writer.Write(w, report)
	return
}
