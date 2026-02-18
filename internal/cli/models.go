package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/providers"
	"github.com/spf13/cobra"
)

var modelsCmd = &cobra.Command{
	Use:   "models",
	Short: "Provider and model management",
}

type modelInfo struct {
	Provider string
	Models   []string
}

var knownModels = []modelInfo{
	{
		Provider: "anthropic",
		Models: []string{
			"claude-sonnet-4-20250514",
			"claude-haiku-4-5-20251001",
			"claude-opus-4-20250514",
		},
	},
	{
		Provider: "openai",
		Models: []string{
			"gpt-4o",
			"gpt-4o-mini",
			"gpt-4-turbo",
			"o1",
			"o1-mini",
		},
	},
	{
		Provider: "gemini",
		Models: []string{
			"gemini-2.0-flash",
			"gemini-2.0-pro",
			"gemini-1.5-flash",
			"gemini-1.5-pro",
		},
	},
}

var modelsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List known providers and models",
	Run: func(cmd *cobra.Command, args []string) {
		for _, info := range knownModels {
			fmt.Fprintf(os.Stdout, "%s:\n", info.Provider)
			for _, m := range info.Models {
				fmt.Fprintf(os.Stdout, "  - %s\n", m)
			}
			fmt.Fprintln(os.Stdout)
		}
	},
}

var modelsDoctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Validate provider credentials",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(buildOverrides())
		if err != nil {
			return err
		}

		providerName := cfg.Provider
		if flagProvider != "" {
			providerName = flagProvider
		}

		fmt.Fprintf(os.Stdout, "Checking %s...\n", providerName)

		p, err := providers.New(providerName, cfg.Model)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
			exitCode = ExitAuthError
			return nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		_, err = p.Review(ctx, providers.ReviewRequest{
			SystemPrompt: "Respond with exactly: ok",
			UserPrompt:   "ping",
			MaxTokens:    10,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
			if providers.IsAuthError(err) {
				exitCode = ExitAuthError
			} else {
				exitCode = ExitRuntimeError
			}
			return nil
		}

		fmt.Fprintf(os.Stdout, "OK: %s is configured and responding\n", providerName)
		return nil
	},
}

func init() {
	modelsCmd.AddCommand(modelsListCmd)
	modelsCmd.AddCommand(modelsDoctorCmd)
	modelsDoctorCmd.Flags().StringVar(&flagProvider, "provider", "", "Provider to check")
}
