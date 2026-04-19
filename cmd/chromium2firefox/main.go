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
	chromiumProfile string
	firefoxProfile  string
	only            string
	reverse         bool
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "chromium2firefox",
		Short: "Losslessly import profiles between Chromium and Firefox",
		Long: `A tool to import history, cookies, favicons, and search engines 
between Chromium-based browsers and Firefox.

By default, it imports from Chromium to Firefox. 
Use --reverse to import from Firefox to Chromium.`,
		Example: `  # Import from Chromium to Firefox (default)
  chromium2firefox -c ~/.config/google-chrome/Default -f ~/.mozilla/firefox/xxx.default

  # Import from Firefox to Chromium
  chromium2firefox --reverse -f ~/.mozilla/firefox/xxx.default -c ~/.config/google-chrome/Default`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImport(cmd)
		},
	}

	rootCmd.PersistentFlags().StringVarP(&chromiumProfile, "chromium-profile", "c", "", "path to the Chromium profile directory")
	rootCmd.PersistentFlags().StringVarP(&firefoxProfile, "firefox-profile", "f", "", "path to the Firefox profile directory")
	rootCmd.PersistentFlags().StringVarP(&only, "only", "o", "", "only import selected data: history,favicons,cookies,search")
	rootCmd.PersistentFlags().BoolVarP(&reverse, "reverse", "r", false, "reverse the conversion direction (Firefox to Chromium)")

	rootCmd.MarkPersistentFlagDirname("chromium-profile")
	rootCmd.MarkPersistentFlagDirname("firefox-profile")

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
	if chromiumProfile == "" || firefoxProfile == "" {
		if err := cmd.Help(); err != nil {
			return err
		}
		return fmt.Errorf("both --chromium-profile and --firefox-profile are required")
	}

	options, err := converter.ParseOnly(only)
	if err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	options.Reverse = reverse

	if err := converter.ConvertProfile(context.Background(), chromiumProfile, firefoxProfile, options); err != nil {
		return fmt.Errorf("convert profile: %w", err)
	}

	fmt.Println("profile conversion completed")
	return nil
}
