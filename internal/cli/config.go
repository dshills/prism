package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/dshills/prism/internal/config"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage prism configuration",
}

var configInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Create a default configuration file",
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := config.ConfigPath()
		if err != nil {
			return err
		}

		if _, err := os.Stat(path); err == nil {
			fmt.Fprintf(os.Stderr, "Config file already exists at %s\n", path)
			return nil
		}

		cfg := config.Default()
		if err := config.Save(cfg); err != nil {
			return fmt.Errorf("writing config: %w", err)
		}

		fmt.Fprintf(os.Stdout, "Config file created at %s\n", path)
		return nil
	},
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a configuration value",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadFile()
		if err != nil {
			// If no config file, start from defaults
			cfg = config.Default()
		}

		if err := config.SetField(&cfg, args[0], args[1]); err != nil {
			return err
		}

		if err := config.Save(cfg); err != nil {
			return fmt.Errorf("saving config: %w", err)
		}

		fmt.Fprintf(os.Stdout, "Set %s = %s\n", args[0], args[1])
		return nil
	},
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show effective configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(nil)
		if err != nil {
			return err
		}

		data, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}

		fmt.Fprintln(os.Stdout, string(data))
		return nil
	},
}

func init() {
	configCmd.AddCommand(configInitCmd)
	configCmd.AddCommand(configSetCmd)
	configCmd.AddCommand(configShowCmd)
}
