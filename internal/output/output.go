package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type Finding struct {
	Rule       string   `json:"rule"`
	Confidence string   `json:"confidence"`
	Message    string   `json:"message"`
	File       string   `json:"file"`
	Line       int      `json:"line"`
	Chain      []string `json:"chain,omitempty"`
}

func Render(w io.Writer, format string, findings []Finding) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(findings)
	case "text", "":
		for _, f := range findings {
			line := fmt.Sprintf("%s:%d: %s [%s] %s", f.File, f.Line, f.Rule, f.Confidence, f.Message)
			if len(f.Chain) > 0 {
				line += " chain=" + strings.Join(f.Chain, " -> ")
			}
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", format)
	}
}
