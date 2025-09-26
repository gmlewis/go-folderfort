// -*- compile-command: "go run main.go"; -*-

// get-or-create-folder gets or creates the named directory in FolderFort using
// its public API: https://na.folderfort.com/api-docs
//
// It is an attempt to use the auto-generated Go SDK client library.
//
// Usage:
//
//	FOLDERFORT_API_TOKEN=abc123 go run cmd/get-or-create-folder/main.go
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"strings"

	"github.com/gmlewis/go-folderfort"
)

const (
	tokenEnvVar = "FOLDERFORT_API_TOKEN"
)

var (
	baseURL    = flag.String("url", "https://na.folderfort.com/api/v1", "FolderFort base API URL")
	folderName = flag.String("folder", "", "Name of folder to get or create on FolderFort")
)

type client struct {
	baseURL string
	fc      *folderfort.Client
}

func main() {
	log.SetFlags(0)
	flag.Parse()

	if *folderName == "" {
		log.Fatalf("Missing -folder flag")
	}

	log.Printf("Using API URL: %v\n", *baseURL)

	apiToken := strings.TrimSpace(os.Getenv(tokenEnvVar))
	if apiToken == "" {
		log.Fatalf("Missing %q env var", tokenEnvVar)
	}
	fc, err := folderfort.NewClientWithAPIToken(*baseURL, apiToken)
	must(err)
	ctx := context.Background()

	parentID, err := fc.GetOrCreateFolder(ctx, strings.TrimSpace(*folderName), nil)
	if err != nil {
		log.Fatalf("Failed to get or create folder: %v", err)
	}
	log.Printf("Folder %q id: %v", *folderName, *parentID)

	log.Printf("Done.")
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
