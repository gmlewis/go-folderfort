package folderfort

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// NewClientWithAPIToken creates a new client that automatically adds
// `Authorization: Bearer <API_TOKEN>` to all requests.
func NewClientWithAPIToken(server, apiToken string) (*Client, error) {
	if server == "" || apiToken == "" {
		return nil, errors.New("missing server or apiToken")
	}

	if !strings.HasPrefix(server, "https://") || (!strings.HasSuffix(server, "/api/v1") && !strings.HasSuffix(server, "/api/v1/")) {
		return nil, errors.New("server must look like: https://na.folderfort.com/api/v1")
	}

	authOpt := func(c *Client) error {
		c.Client = &doerWithToken{apiToken: apiToken}
		return nil
	}

	return NewClient(server, authOpt)
}

type doerWithToken struct {
	apiToken string
}

var _ HttpRequestDoer = &doerWithToken{}

func (d *doerWithToken) Do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+d.apiToken)
	client := &http.Client{}
	return client.Do(req)
}

// FolderResponse represents a response from the CreateFolderWithBody method.
type FolderResponse struct {
	Folder struct {
		ID int64 `json:"id"`
	} `json:"folder"`
}

// CreateNewFolder creates a new folder on FolderFort with an optional parentID.
// On success, it returns the created folder ID.
func (c *Client) CreateNewFolder(ctx context.Context, name string, parentID *int64) (*int64, error) {
	payload := map[string]interface{}{
		"name": name,
	}
	if parentID != nil {
		payload["parentId"] = *parentID
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload for folder '%v': %w", name, err)
	}

	resp, err := c.CreateFolderWithBody(ctx, "application/json", bytes.NewBuffer(payloadBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create folder '%v': %w", name, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("io.ReadAll: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to create folder '%v': %s", name, body)
	}

	var folderResp FolderResponse
	if err := json.Unmarshal(body, &folderResp); err != nil {
		return nil, fmt.Errorf("failed to parse response for folder '%v': %w\n%s", name, err, body)
	}

	folderID := folderResp.Folder.ID
	return &folderID, nil
}

// UploadFileFromPath uploads a file to FolderFort using the provided contentType and folder parentID (or nil for root folder).
// It guesses the mimeType based on the extension of the filePath or defaults to "application/octet-stream".
func (c *Client) UploadFileFromPath(ctx context.Context, filePath string, parentID *int64) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("error opening file %v: %w", filePath, err)
	}
	defer file.Close()

	// Add file field
	fileName := filepath.Base(filePath)
	mimeType := mime.TypeByExtension(filepath.Ext(filePath))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	return c.UploadFile(ctx, fileName, file, mimeType, parentID)
}

// UploadFile uploads a file to FolderFort using the provided contentType and folder parentID (or nil for root folder).
func (c *Client) UploadFile(ctx context.Context, fileName string, r io.Reader, mimeType string, parentID *int64) error {
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

	fmt.Fprintf(writer, "--%v\r\n", boundary)
	fmt.Fprintf(writer, "Content-Disposition: form-data; name=\"file\"; filename=\"%v\"\r\n", fileName)
	fmt.Fprintf(writer, "Content-Type: %v\r\n\r\n", mimeType)

	// Copy file content
	if _, err := io.Copy(writer, r); err != nil {
		return fmt.Errorf("error copying file content: %w", err)
	}

	fmt.Fprintf(writer, "\r\n--%v--\r\n", boundary)

	contentType := "multipart/form-data; boundary=" + boundary
	resp, err := c.UploadWithBody(ctx, contentType, &requestBody)
	if err != nil {
		return fmt.Errorf("failed to upload file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to upload file: %s", body)
	}

	return nil
}

func shouldExclude(path string, excludePatterns []string) bool {
	for _, pattern := range excludePatterns {
		if strings.Contains(path, pattern) {
			return true
		}
	}
	return false
}

func (c *Client) UploadDirectory(ctx context.Context, directoryPath string, parentID *int64, excludePatterns []string) error {
	if excludePatterns == nil {
		excludePatterns = []string{".git", "__pycache__", ".DS_Store", ".env", "venv", "node_modules"}
	}

	entries, err := os.ReadDir(directoryPath)
	if err != nil {
		return fmt.Errorf("error reading directory %v: %w", directoryPath, err)
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
			folderID, err := c.CreateNewFolder(ctx, folderName, parentID)
			if err != nil {
				return err
			}
			log.Printf("Created folder: %v (ID: %v)\n", folderName, *folderID)
			// Recursively upload contents of this folder
			if err := c.UploadDirectory(ctx, itemPath, folderID, excludePatterns); err != nil {
				return err
			}
		} else {
			// Upload file
			if err := c.UploadFileFromPath(ctx, itemPath, parentID); err != nil {
				return err
			}
			// Add a small delay to avoid overwhelming the API
			time.Sleep(500 * time.Millisecond)
		}
	}

	return nil
}
