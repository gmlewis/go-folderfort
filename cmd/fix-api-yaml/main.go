// -*- compile-command: "go run main.go"; -*-

// fix-api-yaml file reads the original "api.yaml.original" file,
// moves the `ref`s into their proper locations to satisfy the
// OpenAPI 3 standard, then writes the file to "api.yaml".
package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	srcFile = flag.String("src", "api.yaml.original", "The original api.yaml source to fix (relative to the repo root)")
	dstFile = flag.String("dst", "api.yaml", "The new fixed api.yaml source to write (relative to the repo root)")
)

// YAMLDocument represents the structure of our YAML document
type YAMLDocument struct {
	Components struct {
		Schemas   map[string]interface{} `yaml:"schemas,omitempty"`
		Responses map[string]interface{} `yaml:"responses,omitempty"`
	} `yaml:"components,omitempty"`
	// Include other fields as a catch-all to preserve the rest of the document
	Other map[string]interface{} `yaml:",inline"`
}

var badRefs = []string{
	"401-Response",
	"403-Response",
}

var pathsToDelete = []string{
	`paths./uploads.post.responses.403.content`,
}

func main() {
	log.SetFlags(0)
	flag.Parse()

	_, thisDir, _, _ := runtime.Caller(0)
	srcPath := filepath.Join(filepath.Dir(thisDir), "../..", *srcFile)
	dstPath := filepath.Join(filepath.Dir(thisDir), "../..", *dstFile)

	// Read the YAML file
	data, err := ioutil.ReadFile(srcPath)
	must(err)

	yamlText := string(data)

	for _, badRef := range badRefs {
		yamlText = fixBadRef(yamlText, badRef)
	}

	// Step 2: Parse the YAML to move the definition
	var rootNode yaml.Node
	must(yaml.Unmarshal([]byte(yamlText), &rootNode))

	for _, badRef := range badRefs {
		// Find and move the badRef definition
		moveResponse(&rootNode, badRef)
	}

	for _, dddPath := range pathsToDelete {
		// Delete the node at the specified path
		deleteNodeAtPath(&rootNode, dddPath)
	}

	// Step 3: Marshal back to YAML with preserved formatting
	var buf strings.Builder
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	err = encoder.Encode(&rootNode)
	encoder.Close()
	if err != nil {
		log.Fatalf("Error marshaling YAML: %v", err)
	}

	// Step 4: Write back to file
	must(ioutil.WriteFile(dstPath, []byte(buf.String()), 0644))

	log.Printf("Done.")
}

func fixBadRef(yamlText, badRef string) string {
	// Step 1: Fix all references from schemas to responses
	oldRef := "#/components/schemas/" + badRef
	newRef := "#/components/responses/" + badRef

	// Count occurrences for reporting
	refCount := strings.Count(yamlText, oldRef)
	if refCount > 0 {
		yamlText = strings.ReplaceAll(yamlText, oldRef, newRef)
		fmt.Printf("Fixed %v references from %v to %v\n", refCount, oldRef, newRef)
	} else {
		fmt.Println("No references to fix found")
	}

	return yamlText
}

// moveResponse finds the badRef in components/schemas and moves it to components/responses
func moveResponse(node *yaml.Node, badRef string) {
	// Find the root document node
	var docNode *yaml.Node
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		docNode = node.Content[0]
	} else if node.Kind == yaml.MappingNode {
		docNode = node
	} else {
		log.Fatal("Could not find the docNode")
	}

	// Find components section
	componentsNode := findMapValue(docNode, "components")
	if componentsNode == nil {
		log.Fatal("No 'components' section found")
	}

	// Find schemas section
	schemasNode := findMapValue(componentsNode, "schemas")
	if schemasNode == nil {
		log.Fatal("No 'components/schemas' section found")
	}

	// Find and extract badRef from schemas
	response401Node := extractMapValue(schemasNode, badRef)
	if response401Node == nil {
		log.Fatalf("%q not found in components/schemas", badRef)
	}

	// Find or create responses section
	responsesNode := findMapValue(componentsNode, "responses")
	if responsesNode == nil {
		// Create responses section
		responsesNode = &yaml.Node{
			Kind: yaml.MappingNode,
		}
		// Add to components
		componentsNode.Content = append(componentsNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "responses"},
			responsesNode)
	}

	// Add badRef to responses section
	responsesNode.Content = append(responsesNode.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: badRef},
		response401Node)

	log.Printf("Successfully moved %q from schemas to responses", badRef)
}

// findMapValue finds a key in a mapping node and returns its value node
func findMapValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// extractMapValue finds and removes a key from a mapping node, returning its value node
func extractMapValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			// Found the key, extract its value
			valueNode := node.Content[i+1]
			// Remove both key and value from the slice
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return valueNode
		}
	}
	return nil
}

// deleteNodeAtPath deletes a node at the specified DDD-style path
func deleteNodeAtPath(rootNode *yaml.Node, dddPath string) {
	// Find the document node
	var docNode *yaml.Node
	if rootNode.Kind == yaml.DocumentNode && len(rootNode.Content) > 0 {
		docNode = rootNode.Content[0]
	} else if rootNode.Kind == yaml.MappingNode {
		docNode = rootNode
	} else {
		log.Fatal("Could not find the docNode")
	}

	// Parse the path into segments
	segments := parseDDDPath(dddPath)
	if len(segments) == 0 {
		log.Fatalf("ddd path %q not found", dddPath)
	}

	// Navigate to the parent of the target node
	currentNode := docNode
	for i := 0; i < len(segments)-1; i++ {
		nextNode := findMapValue(currentNode, segments[i])
		if nextNode == nil {
			log.Fatalf("Path segment %q not found\n", segments[i])
		}
		currentNode = nextNode
	}

	// Delete the final key from the parent node
	finalKey := segments[len(segments)-1]
	deleteMapKey(currentNode, finalKey)
}

// parseDDDPath parses a DDD-style path into segments
// handles: paths./uploads.post.responses.403.content (escaped)
func parseDDDPath(path string) []string {
	var segments []string
	var current strings.Builder
	inBrackets := false
	inQuotes := false
	escapeNext := false

	for _, char := range path {
		if escapeNext {
			current.WriteRune(char)
			escapeNext = false
			continue
		}

		switch char {
		case '\\':
			if !inBrackets {
				escapeNext = true
			} else {
				current.WriteRune(char)
			}
		case '[':
			if !inBrackets {
				inBrackets = true
			} else {
				current.WriteRune(char)
			}
		case ']':
			if inBrackets && !inQuotes {
				inBrackets = false
			} else {
				current.WriteRune(char)
			}
		case '"', '\'':
			if inBrackets {
				inQuotes = !inQuotes
			} else {
				current.WriteRune(char)
			}
		case '.':
			if !inBrackets {
				// End of segment
				segment := strings.TrimSpace(current.String())
				if segment != "" {
					segments = append(segments, segment)
				}
				current.Reset()
			} else {
				current.WriteRune(char)
			}
		default:
			current.WriteRune(char)
		}
	}

	// Add the final segment
	segment := strings.TrimSpace(current.String())
	if segment != "" {
		// Remove quotes if present
		if (strings.HasPrefix(segment, "\"") && strings.HasSuffix(segment, "\"")) ||
			(strings.HasPrefix(segment, "'") && strings.HasSuffix(segment, "'")) {
			segment = segment[1 : len(segment)-1]
		}
		segments = append(segments, segment)
	}

	return segments
}

// deleteMapKey deletes a key-value pair from a mapping node
func deleteMapKey(node *yaml.Node, key string) {
	if node == nil || node.Kind != yaml.MappingNode {
		log.Fatal("bad node")
	}

	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			// Remove both key and value from the slice
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			log.Printf("Deleted key %q\n", key)
			return
		}
	}

	log.Fatalf("Could not find key %q", key)
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
