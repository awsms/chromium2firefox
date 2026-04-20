package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/awsms/chromium2firefox/internal/converter"

	"github.com/spf13/cobra"
)

var (
	sourceDir string
	destDir   string
	only      string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "chromium2firefox",
		Short: "Losslessly import profiles between Chromium and Firefox",
		Long: `A tool to import history, cookies, favicons, and search engines 
between Chromium-based browsers and Firefox, or between two Chromium browsers.

The tool automatically detects the browser type (Chromium or Firefox) 
based on the contents of the provided profile directories.`,
		Example: `  # Import from Chromium to Firefox (auto-detected)
  chromium2firefox -s ~/.config/google-chrome/Default -d ~/.mozilla/firefox/xxx.default

  # Import from Firefox to Chromium (auto-detected)
  chromium2firefox -s ~/.mozilla/firefox/xxx.default -d ~/.config/google-chrome/Default

  # Import from Chromium to another Chromium browser (auto-detected)
  chromium2firefox -s ~/.config/google-chrome/Default -d ~/.config/brave-browser/Default`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImport(cmd)
		},
	}

	rootCmd.PersistentFlags().StringVarP(&sourceDir, "source", "s", "", "path to the source profile directory")
	rootCmd.PersistentFlags().StringVarP(&destDir, "dest", "d", "", "path to the destination profile directory")
	rootCmd.PersistentFlags().StringVarP(&only, "only", "o", "", "only import selected data: history,favicons,cookies,search")

	rootCmd.MarkPersistentFlagDirname("source")
	rootCmd.MarkPersistentFlagDirname("dest")

	// Explicitly add a version command
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version number",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("chromium2firefox v1.0.0")
		},
	})

	// Add autocompletion for the "only" flag
	rootCmd.RegisterFlagCompletionFunc("only", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		options := []string{"history", "favicons", "cookies", "search"}
		parts := strings.Split(toComplete, ",")
		lastPart := parts[len(parts)-1]

		var completions []string
		prefix := strings.Join(parts[:len(parts)-1], ",")
		if prefix != "" {
			prefix += ","
		}

		for _, opt := range options {
			if strings.HasPrefix(opt, lastPart) {
				alreadySelected := false
				for i := 0; i < len(parts)-1; i++ {
					if parts[i] == opt {
						alreadySelected = true
						break
					}
				}
				if !alreadySelected {
					completions = append(completions, prefix+opt)
				}
			}
		}
		return completions, cobra.ShellCompDirectiveNoSpace
	})

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runImport(cmd *cobra.Command) error {
	if sourceDir == "" || destDir == "" {
		if err := cmd.Help(); err != nil {
			return err
		}
		return fmt.Errorf("both --source and --dest are required")
	}

	options, err := converter.ParseOnly(only)
	if err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	if err := converter.ConvertProfile(context.Background(), sourceDir, destDir, options); err != nil {
		return fmt.Errorf("convert profile: %w", err)
	}

	fmt.Println("profile conversion completed")
	return nil
}
