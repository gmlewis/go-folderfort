// -*- compile-command: "go run main.go"; -*-

// upload-directory uploads the named directory to FolderFort using
// its public API: https://na.folderfort.com/api-docs
//
// It is an attempt to use the auto-generated Go SDK client library.
//
// Usage:
//
//	FOLDERFORT_API_TOKEN=abc123 go run cmd/upload-directory/main.go
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
	dirName    = flag.String("dir", ".", "Directory to upload to FolderFort")
	folderName = flag.String("folder", "", "Optional name of new folder to create on FolderFort")
)

type client struct {
	baseURL string
	fc      *folderfort.Client
}

func main() {
	log.SetFlags(0)
	flag.Parse()
	log.Printf("Using API URL: %v\n", *baseURL)

	apiToken := strings.TrimSpace(os.Getenv(tokenEnvVar))
	if apiToken == "" {
		log.Fatalf("Missing %q env var", tokenEnvVar)
	}
	fc, err := folderfort.NewClientWithAPIToken(*baseURL, apiToken)
	must(err)
	ctx := context.Background()

	log.Printf("Uploading directory: %v\n", *dirName)

	var parentID *int64 // Root folder
	if *folderName != "" {
		parentID, err = fc.CreateNewFolder(ctx, strings.TrimSpace(*folderName), nil)
		if err != nil {
			log.Fatalf("Failed to create parent folder: %v", err)
		}
	}

	// Start uploading
	fc.UploadDirectory(ctx, *dirName, parentID, nil)

	log.Printf("Done.")
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
