package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/dshills/prism/internal/cache"
	"github.com/dshills/prism/internal/config"
	"github.com/spf13/cobra"
)

var cacheCmd = &cobra.Command{
	Use:   "cache",
	Short: "Manage the review cache",
}

var cacheClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear all cached review results",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(nil)
		if err != nil {
			return err
		}
		c, err := cache.New(true, cfg.Cache.Dir, cfg.Cache.TTLSeconds)
		if err != nil {
			return fmt.Errorf("opening cache: %w", err)
		}
		if err := c.Clear(); err != nil {
			return fmt.Errorf("clearing cache: %w", err)
		}
		fmt.Fprintln(os.Stdout, "Cache cleared.")
		return nil
	},
}

var cacheShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show cache statistics",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(nil)
		if err != nil {
			return err
		}
		c, err := cache.New(cfg.Cache.Enabled, cfg.Cache.Dir, cfg.Cache.TTLSeconds)
		if err != nil {
			return fmt.Errorf("opening cache: %w", err)
		}
		if !c.Enabled() {
			fmt.Fprintln(os.Stdout, "Cache is disabled.")
			return nil
		}
		stats, err := c.GetStats()
		if err != nil {
			return fmt.Errorf("reading cache stats: %w", err)
		}
		data, err := json.MarshalIndent(stats, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, string(data))
		return nil
	},
}

func init() {
	cacheCmd.AddCommand(cacheClearCmd)
	cacheCmd.AddCommand(cacheShowCmd)
}
