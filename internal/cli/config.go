package cli

import (
	"fmt"
	"slices"

	"github.com/spf13/cobra"

	"github.com/alnah/transcript/internal/config"
)

// validConfigKeys lists all supported configuration keys.
var validConfigKeys = []string{
	config.KeyOutputDir,
}

// ConfigCmd creates the config command with subcommands.
// The env parameter provides injectable dependencies for testing.
func ConfigCmd(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration settings",
		Long: `Manage persistent configuration settings.

Configuration is stored in ~/.config/transcript/config.
Settings can also be overridden via environment variables.

Supported settings:
  output-dir    Default directory for output files (env: TRANSCRIPT_OUTPUT_DIR)`,
		Example: `  transcript config set output-dir ~/Documents/transcripts
  transcript config get output-dir
  transcript config list`,
	}

	cmd.AddCommand(configSetCmd(env))
	cmd.AddCommand(configGetCmd(env))
	cmd.AddCommand(configListCmd(env))

	return cmd
}

// configSetCmd creates the "config set" subcommand.
func configSetCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a configuration value",
		Long: `Set a configuration value.

Supported keys:
  output-dir    Default directory for output files

The directory will be created if it doesn't exist.`,
		Example: `  transcript config set output-dir ~/Documents/transcripts
  transcript config set output-dir /tmp/recordings`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]
			return runConfigSet(env, key, value)
		},
	}
}

// configGetCmd creates the "config get" subcommand.
func configGetCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Get a configuration value",
		Long: `Get a configuration value.

Prints the value to stdout, or nothing if not set.`,
		Example: `  transcript config get output-dir`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigGet(env, args[0])
		},
	}
}

// configListCmd creates the "config list" subcommand.
func configListCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all configuration values",
		Long: `List all configuration values.

Shows both values from the config file and environment variable overrides.`,
		Example: `  transcript config list`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigList(env)
		},
	}
}

// runConfigSet handles the "config set" command.
func runConfigSet(env *Env, key, value string) error {
	// Validate key.
	if !isValidConfigKey(key) {
		return fmt.Errorf("unknown config key %q (valid keys: %v)", key, validConfigKeys)
	}

	// Key-specific validation.
	switch key {
	case config.KeyOutputDir:
		// Expand ~ and validate directory.
		expanded := config.ExpandPath(value)
		if err := config.EnsureOutputDir(expanded); err != nil {
			return fmt.Errorf("invalid output-dir: %w", err)
		}
		// Store the expanded path for consistency.
		value = expanded
	}

	// Save to config file.
	if err := config.Save(key, value); err != nil {
		return err
	}

	fmt.Fprintf(env.Stderr, "Set %s = %s\n", key, value)
	return nil
}

// runConfigGet handles the "config get" command.
func runConfigGet(env *Env, key string) error {
	// Validate key.
	if !isValidConfigKey(key) {
		return fmt.Errorf("unknown config key %q (valid keys: %v)", key, validConfigKeys)
	}

	value, err := config.Get(key)
	if err != nil {
		return err
	}

	// Check environment variable fallback.
	if value == "" {
		switch key {
		case config.KeyOutputDir:
			value = env.Getenv(config.EnvOutputDir)
		}
	}

	if value != "" {
		fmt.Println(value)
	}

	return nil
}

// runConfigList handles the "config list" command.
func runConfigList(env *Env) error {
	data, err := config.List()
	if err != nil {
		return err
	}

	// Add environment variable values for completeness.
	if _, ok := data[config.KeyOutputDir]; !ok {
		if envVal := env.Getenv(config.EnvOutputDir); envVal != "" {
			data[config.KeyOutputDir] = envVal + " (from env)"
		}
	}

	if len(data) == 0 {
		fmt.Println("No configuration set.")
		fmt.Println("\nAvailable settings:")
		for _, key := range validConfigKeys {
			fmt.Printf("  %s\n", key)
		}
		return nil
	}

	// Sort keys for deterministic output.
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	for _, key := range keys {
		fmt.Printf("%s=%s\n", key, data[key])
	}

	return nil
}

// isValidConfigKey checks if a key is a valid configuration key.
func isValidConfigKey(key string) bool {
	return slices.Contains(validConfigKeys, key)
}
