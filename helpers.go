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

	"github.com/gmlewis/go-httpdebug/httpdebug"
)

// NewClientWithAPIToken creates a new client that automatically adds
// `Authorization: Bearer <API_TOKEN>` to all requests.
func NewClientWithAPIToken(server, apiToken string, debug bool) (*Client, error) {
	if server == "" || apiToken == "" {
		return nil, errors.New("missing server or apiToken")
	}

	if !strings.HasPrefix(server, "https://") || (!strings.HasSuffix(server, "/api/v1") && !strings.HasSuffix(server, "/api/v1/")) {
		return nil, errors.New("server must look like: https://na.folderfort.com/api/v1")
	}

	authOpt := func(c *Client) error {
		c.Client = &doerWithToken{apiToken: apiToken, debug: debug}
		return nil
	}

	return NewClient(server, authOpt)
}

type doerWithToken struct {
	apiToken string
	debug    bool
}

var _ HttpRequestDoer = &doerWithToken{}

func (d *doerWithToken) Do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+d.apiToken)
	client := &http.Client{}
	if d.debug {
		ct := httpdebug.New()
		client = &http.Client{Transport: ct}
	}
	return client.Do(req)
}

type indexEntryResponseT struct {
	Data []struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		FileName string `json:"file_name"`
		ParentID int64  `json:"parent_id"`
		Path     string `json:"path"`
	} `json:"data"`
}

// Ptr returns a pointer to the provided value.
func Ptr[T any](v T) *T {
	return &v
}

// DeleteEntries deletes entries by ID.
func (c *Client) DeleteEntries(ctx context.Context, ids []string) error {
	// curl -X POST ' https://na.folderfort.com/api/v1/file-entries' \
	// -H 'Authorization: Bearer YOUR_ACCESS_TOKEN' \
	// -H 'accept: application/json' \
	// -H 'Content-Type: application/json' \
	// -H 'X-HTTP-Method-Override: DELETE' \
	// --data '{"entryIds":[12345],"deleteForever":false}'
	log.Printf("GML: DeleteEntries(ids=%+v)", ids)

	req := EntriesDeleteJSONRequestBody{
		EntryIds: &ids,
	}

	resp, err := c.EntriesDelete(ctx, req)
	if err != nil {
		return fmt.Errorf("c.EntriesDelete: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("io.ReadAll: %w", err)
	}

	if resp.StatusCode != 200 {
		return fmt.Errorf("failed to delete entries %+v: %s", ids, body)
	}

	return nil
}

// getEntriesByName queries FolderFort to see if one or more named entries exist within the provided parentID.
// An optional type can be provided to narrow the search.
func (c *Client) getEntriesByName(ctx context.Context, name string, parentID *int64, typ *IndexEntryParamsType) ([]int64, error) {
	// log.Printf("GML: getEntriesByName(name=%q, parentID=%#v)", name, parentID)

	params := &IndexEntryParams{
		Query: &name,
		Type:  typ, // OK if nil
	}
	if parentID != nil {
		parentIDs := []string{fmt.Sprintf("%v", *parentID)}
		params.ParentIds = &parentIDs
	}

	resp, err := c.IndexEntry(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("c.IndexEntry: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("io.ReadAll: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to get folder '%v': %s", name, body)
	}

	var indexEntryResp indexEntryResponseT
	if err := json.Unmarshal(body, &indexEntryResp); err != nil {
		return nil, fmt.Errorf("failed to parse response for folder '%v': %w\n%s", name, err, body)
	}

	var results []int64
	for _, v := range indexEntryResp.Data {
		if parentID != nil && *parentID != v.ParentID {
			log.Printf("GML: getEntriesByName: QUERY IGNORED ParentIDs!: Name=%q, ID=%v, ParentID=%v, FileName=%q, Path=%q", v.Name, v.ID, v.ParentID, v.FileName, v.Path)
			continue
		}
		if v.Name == name {
			log.Printf("GML: getEntriesByName: FOUND MATCH: Name=%q, ID=%v, ParentID=%v, FileName=%q, Path=%q", v.Name, v.ID, v.ParentID, v.FileName, v.Path)
			results = append(results, v.ID)
		}
	}

	return results, nil
}

// getFolder queries FolderFort to see if the named folder exists within the provided parentID.
// It does not support parent folders (e.g. "parent/folder-name").
func (c *Client) getFolder(ctx context.Context, name string, parentID *int64) (*int64, error) {
	// log.Printf("GML: getFolder(name=%q, parentID=%#v)", name, parentID)

	ids, err := c.getEntriesByName(ctx, name, parentID, Ptr(IndexEntryParamsTypeFolder))
	if err != nil {
		return nil, err
	}

	if len(ids) > 0 {
		return &ids[0], nil
	}

	return nil, fmt.Errorf("unable to find folder %q", name)
}

type createFolderWithBodyResponse struct {
	Folder struct {
		ID int64 `json:"id"`
	} `json:"folder"`
}

// GetOrCreateFolder gets or creates a folder on FolderFort starting with an optional parentID.
// On success, it returns the created folder ID. It recursively creates all intermediate folders if needed.
func (c *Client) GetOrCreateFolder(ctx context.Context, name string, parentID *int64) (*int64, error) {
	// log.Printf("GML: GetOrCreateFolder(name=%q, parentID=%#v)", name, parentID)

	if name == "" {
		return nil, errors.New("name must not be empty")
	}

	parentDir, baseDir := filepath.Split(name)
	parentDir = strings.TrimSuffix(parentDir, "/")
	if parentDir != "" {
		var err error
		parentID, err = c.GetOrCreateFolder(ctx, parentDir, parentID)
		name = baseDir
		if err != nil {
			return nil, fmt.Errorf("unable to create folder %q: %w", parentDir, err)
		}
	}

	// Check to see if this folder already exists. If not, create it.
	if id, err := c.getFolder(ctx, name, parentID); err == nil {
		return id, nil
	}

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

	var folderResp createFolderWithBodyResponse
	if err := json.Unmarshal(body, &folderResp); err != nil {
		return nil, fmt.Errorf("failed to parse response for folder '%v': %w\n%s", name, err, body)
	}

	folderID := folderResp.Folder.ID
	return &folderID, nil
}

// UploadFileFromPath uploads a file to FolderFort using the provided contentType and folder parentID (or nil for root folder).
// It guesses the mimeType based on the extension of the filePath or defaults to "application/octet-stream".
// If overwrite is true, then any existing files of the same name in the same folder will first be deleted.
func (c *Client) UploadFileFromPath(ctx context.Context, filePath string, parentID *int64, overwrite bool) error {
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

	return c.UploadFile(ctx, fileName, file, mimeType, parentID, overwrite)
}

// UploadFile uploads a file to FolderFort using the provided contentType and folder parentID (or nil for root folder).
// If fileName contains parent folder(s), it recursively creates all intermediate folders if needed.
// If overwrite is true, then any existing files of the same name in the same folder will first be deleted.
func (c *Client) UploadFile(ctx context.Context, fileName string, r io.Reader, mimeType string, parentID *int64, overwrite bool) error {
	// log.Printf("GML: UploadFile(fileName=%q, mimeType=%q, parentID=%#v)", fileName, mimeType, parentID)

	if fileName == "" {
		return errors.New("fileName must not be empty")
	}

	parentDir, baseName := filepath.Split(fileName)
	parentDir = strings.TrimSuffix(parentDir, "/")
	if parentDir != "" {
		var err error
		parentID, err = c.GetOrCreateFolder(ctx, parentDir, parentID)
		fileName = baseName
		if err != nil {
			return fmt.Errorf("unable to create folder %q: %w", parentDir, err)
		}
	}

	if overwrite {
		ids, err := c.getEntriesByName(ctx, fileName, parentID, nil)
		if err != nil {
			log.Printf("getEntriesByName: %v (ignoring)", err)
		} else if len(ids) > 0 {
			strIDs := make([]string, 0, len(ids))
			for _, id := range ids {
				strIDs = append(strIDs, fmt.Sprintf("%v", id))
			}
			if err := c.DeleteEntries(ctx, strIDs); err != nil {
				log.Printf("c.DeleteEntries(ids=%+v): %v (ignoring)", ids, err)
			}
		}
	}

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

// UploadDirectory uploads the contents of a directory to FolderFort.
// If any filename already exists, it is overwritten.
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
			// Get or create folder
			folderName := entry.Name()
			folderID, err := c.GetOrCreateFolder(ctx, folderName, parentID)
			if err != nil {
				return err
			}
			// log.Printf("GML: folder: %v (ID: %v)\n", folderName, *folderID)
			// Recursively upload contents of this folder
			if err := c.UploadDirectory(ctx, itemPath, folderID, excludePatterns); err != nil {
				return err
			}
		} else {
			// Upload file
			if err := c.UploadFileFromPath(ctx, itemPath, parentID, true); err != nil {
				return err
			}
			// Add a small delay to avoid overwhelming the API
			time.Sleep(500 * time.Millisecond)
		}
	}

	return nil
}
