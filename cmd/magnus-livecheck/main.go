// Command magnus-livecheck runs the CONTRACT.md checklist against a real
// Magnus deployment and exits non-zero unless every check passes.
//
//	MAGNUS_BASE_URL=https://... MAGNUS_API_KEY=magnus_... magnus-livecheck
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/MeGrimlock/magnus-go-sdk/livecheck"
)

func main() {
	baseURL := flag.String("base-url", os.Getenv("MAGNUS_BASE_URL"), "Magnus server root")
	apiKey := flag.String("api-key", os.Getenv("MAGNUS_API_KEY"), "System or User API Key")
	agent := flag.String("agent", os.Getenv("MAGNUS_AGENT"),
		"agent id to test against (default: the first one listed)")
	prompt := flag.String("prompt", "", "the message each turn sends")
	timeout := flag.Duration("timeout", 90*time.Second, "per-request timeout")
	noColor := flag.Bool("no-color", false, "plain output, for CI logs")
	flag.Parse()

	if *baseURL == "" || *apiKey == "" {
		fmt.Fprintln(os.Stderr,
			"magnus-livecheck: MAGNUS_BASE_URL and MAGNUS_API_KEY are required "+
				"(or pass -base-url / -api-key).")
		os.Exit(2)
	}

	os.Exit(livecheck.Run(context.Background(), livecheck.Options{
		BaseURL: *baseURL,
		APIKey:  *apiKey,
		Agent:   *agent,
		Prompt:  *prompt,
		Timeout: *timeout,
		Color:   !*noColor,
		Out:     os.Stdout,
	}))
}
