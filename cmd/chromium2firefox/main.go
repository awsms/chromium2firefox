package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"chromium2firefox/internal/converter"
)

func main() {
	var (
		chromiumProfile = flag.String("chromium-profile", "", "path to the Chromium profile directory")
		firefoxProfile  = flag.String("firefox-profile", "", "path to the Firefox profile directory")
	)
	flag.Parse()

	if *chromiumProfile == "" || *firefoxProfile == "" {
		flag.Usage()
		os.Exit(2)
	}

	if err := converter.ConvertProfile(context.Background(), *chromiumProfile, *firefoxProfile); err != nil {
		log.Fatalf("convert history: %v", err)
	}

	fmt.Println("history conversion completed")
}
