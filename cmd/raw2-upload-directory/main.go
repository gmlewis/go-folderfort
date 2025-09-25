// -*- compile-command: "go run main.go"; -*-

// raw2-upload-directory uploads the named directory to FolderFort using
// its public API: https://na.folderfort.com/api-docs
//
// It is an attempt to use the auto-generated Go SDK client library.
//
// Usage:
//
//	FOLDERFORT_API_TOKEN=abc123 go run cmd/raw2-upload-directory/main.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	c := &client{baseURL: *baseURL, fc: fc}
	ctx := context.Background()

	log.Printf("Uploading directory: %v\n", *dirName)

	var parentID *int64 // Root folder
	if *folderName != "" {
		parentID = c.createFolder(ctx, strings.TrimSpace(*folderName), nil)
		if parentID == nil {
			log.Printf("Failed to create parent folder. Uploading to root instead.")
		}
	}

	// Start uploading
	c.uploadDirectory(ctx, *dirName, parentID, nil)

	log.Printf("Done.")
}

type FolderResponse struct {
	Folder struct {
		ID int64 `json:"id"`
	} `json:"folder"`
}

func (c *client) createFolder(ctx context.Context, name string, parentID *int64) *int64 {
	payload := map[string]interface{}{
		"name": name,
	}
	if parentID != nil {
		payload["parentId"] = *parentID
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Failed to marshal payload for folder '%v': %v\n", name, err)
		return nil
	}

	resp, err := c.fc.CreateFolderWithBody(ctx, "application/json", bytes.NewBuffer(payloadBytes))
	if err != nil {
		log.Printf("Failed to create folder '%v': %v\n", name, err)
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	must(err)

	if resp.StatusCode != 200 {
		log.Printf("Failed to create folder '%v': %v\n", name, string(body))
		return nil
	}

	var folderResp FolderResponse
	if err := json.Unmarshal(body, &folderResp); err != nil {
		log.Printf("Failed to parse response for folder '%v': %v\n%s", name, err, body)
		return nil
	}

	folderID := folderResp.Folder.ID
	return &folderID
}

func (c *client) uploadFile(ctx context.Context, filePath string, parentID *int64) bool {
	file, err := os.Open(filePath)
	if err != nil {
		log.Printf("Error opening file %v: %v\n", filePath, err)
		return false
	}
	defer file.Close()

	// Create a buffer to store our request body
	var requestBody bytes.Buffer
	writer := io.Writer(&requestBody)

	// Create multipart form
	boundary := "----WebKitFormBoundary7MA4YWxkTrZu0gW"

	// Add parentId field if provided
	if parentID != nil {
		fmt.Fprintf(writer, "--%v\r\n", boundary)
		fmt.Fprintf(writer, "Content-Disposition: form-data; name=\"parentId\"\r\n\r\n")
		fmt.Fprintf(writer, "%v\r\n", *parentID)
	}

	// Add file field
	fileName := filepath.Base(filePath)
	mimeType := mime.TypeByExtension(filepath.Ext(filePath))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	fmt.Fprintf(writer, "--%v\r\n", boundary)
	fmt.Fprintf(writer, "Content-Disposition: form-data; name=\"file\"; filename=\"%v\"\r\n", fileName)
	fmt.Fprintf(writer, "Content-Type: %v\r\n\r\n", mimeType)

	// Copy file content
	if _, err := io.Copy(writer, file); err != nil {
		log.Printf("Error copying file content %v: %v\n", filePath, err)
		return false
	}

	fmt.Fprintf(writer, "\r\n--%v--\r\n", boundary)

	contentType := "multipart/form-data; boundary=" + boundary
	resp, err := c.fc.UploadWithBody(ctx, contentType, &requestBody)
	if err != nil {
		log.Printf("Failed to upload file '%v': %v\n", filePath, err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("Failed to upload file '%v': %v\n", filePath, string(body))
		return false
	}

	log.Printf("Successfully uploaded: %v\n", filePath)
	return true
}

func shouldExclude(path string, excludePatterns []string) bool {
	for _, pattern := range excludePatterns {
		if strings.Contains(path, pattern) {
			return true
		}
	}
	return false
}

func (c *client) uploadDirectory(ctx context.Context, directoryPath string, parentID *int64, excludePatterns []string) {
	if excludePatterns == nil {
		excludePatterns = []string{".git", "__pycache__", ".DS_Store", ".env", "venv", "node_modules"}
	}

	entries, err := os.ReadDir(directoryPath)
	if err != nil {
		log.Printf("Error reading directory %v: %v\n", directoryPath, err)
		return
	}

	for _, entry := range entries {
		itemPath := filepath.Join(directoryPath, entry.Name())

		// Skip excluded patterns
		if shouldExclude(itemPath, excludePatterns) {
			continue
		}

		// Skip large files (100MB limit)
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				log.Printf("Error getting file info for %v: %v\n", itemPath, err)
				continue
			}
			if info.Size() > 100*1024*1024 {
				log.Printf("Skipping large file: %v (%.2f MB)\n", itemPath, float64(info.Size())/(1024*1024))
				continue
			}
		}

		if entry.IsDir() {
			// Create folder
			folderName := entry.Name()
			folderID := c.createFolder(ctx, folderName, parentID)
			if folderID != nil {
				log.Printf("Created folder: %v (ID: %v)\n", folderName, *folderID)
				// Recursively upload contents of this folder
				c.uploadDirectory(ctx, itemPath, folderID, excludePatterns)
			}
		} else {
			// Upload file
			c.uploadFile(ctx, itemPath, parentID)
			// Add a small delay to avoid overwhelming the API
			time.Sleep(500 * time.Millisecond)
		}
	}
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
