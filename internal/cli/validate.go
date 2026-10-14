package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/baumai/baum/bundle"
	"github.com/baumai/baum/review"
)

func runValidate(cmd *cobra.Command, documentPath, bundlePath string) error {
	document, err := os.ReadFile(documentPath)
	if err != nil {
		return fmt.Errorf("read document: %w", err)
	}
	var generation *bundle.Bundle
	if bundlePath != "" {
		raw, err := os.ReadFile(bundlePath)
		if err != nil {
			return fmt.Errorf("read bundle: %w", err)
		}
		generation = &bundle.Bundle{}
		if err := json.Unmarshal(raw, generation); err != nil {
			return fmt.Errorf("decode bundle %s: %w", bundlePath, err)
		}
	}
	validator, err := review.NewDocumentValidator()
	if err != nil {
		return err
	}
	findings := validator.ValidateStored(document, generation)
	if len(findings) == 0 {
		return nil
	}
	for _, finding := range findings {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), finding)
	}
	noun := "findings"
	if len(findings) == 1 {
		noun = "finding"
	}
	return fmt.Errorf("%s is invalid: %d %s", documentPath, len(findings), noun)
}
