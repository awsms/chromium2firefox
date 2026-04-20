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
	sourceChromium string
	sourceFirefox  string
	targetChromium string
	only           string
	reverse        bool
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "chromium2firefox",
		Short: "Losslessly import profiles between Chromium and Firefox",
		Long: `A tool to import history, cookies, favicons, and search engines 
between Chromium-based browsers and Firefox, or between two Chromium browsers.

By default, it imports from Chromium to Firefox. 
Use --reverse to import from Firefox to Chromium.
Use --to-chromium to import from Chromium to another Chromium browser.`,
		Example: `  # Import from Chromium to Firefox (default)
  chromium2firefox -c ~/.config/google-chrome/Default -f ~/.mozilla/firefox/xxx.default

  # Import from Firefox to Chromium
  chromium2firefox --reverse -f ~/.mozilla/firefox/xxx.default -c ~/.config/google-chrome/Default

  # Import from Chromium to another Chromium browser
  chromium2firefox -c ~/.config/google-chrome/Default --to-chromium ~/.config/brave-browser/Default`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImport(cmd)
		},
	}

	rootCmd.PersistentFlags().StringVarP(&sourceChromium, "chromium-profile", "c", "", "path to the (source) Chromium profile directory")
	rootCmd.PersistentFlags().StringVarP(&sourceFirefox, "firefox-profile", "f", "", "path to the (source/target) Firefox profile directory")
	rootCmd.PersistentFlags().StringVarP(&targetChromium, "to-chromium", "t", "", "path to the target Chromium profile directory")
	rootCmd.PersistentFlags().StringVarP(&only, "only", "o", "", "only import selected data: history,favicons,cookies,search")
	rootCmd.PersistentFlags().BoolVarP(&reverse, "reverse", "r", false, "reverse the conversion direction (Firefox to Chromium)")

	rootCmd.MarkPersistentFlagDirname("chromium-profile")
	rootCmd.MarkPersistentFlagDirname("firefox-profile")
	rootCmd.MarkPersistentFlagDirname("to-chromium")

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
	if sourceChromium == "" && sourceFirefox == "" {
		if err := cmd.Help(); err != nil {
			return err
		}
		return fmt.Errorf("at least one source profile (--chromium-profile or --firefox-profile) is required")
	}

	if targetChromium == "" && sourceFirefox == "" {
		return fmt.Errorf("a target profile (--to-chromium or --firefox-profile) is required")
	}

	options, err := converter.ParseOnly(only)
	if err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	options.Reverse = reverse

	if err := converter.ConvertProfile(context.Background(), sourceChromium, sourceFirefox, targetChromium, sourceFirefox, options); err != nil {
		return fmt.Errorf("convert profile: %w", err)
	}

	fmt.Println("profile conversion completed")
	return nil
}
