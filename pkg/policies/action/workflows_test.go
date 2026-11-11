package action

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-github/v84/github"
)

func TestListWorkflowsRealEnumeratesAllWorkflows(t *testing.T) {
	t.Parallel()

	files := makeWorkflowFixtureFiles(51)
	server := newWorkflowFixtureServer(t, files, len(files))
	defer server.Close()

	client := newWorkflowFixtureClient(t, server)
	workflows, err := listWorkflowsReal(t.Context(), client, "owner", "repo")
	if err != nil {
		t.Fatalf("listWorkflowsReal returned error: %v", err)
	}
	if len(workflows) != len(files) {
		t.Fatalf("got %d workflows, want %d", len(workflows), len(files))
	}
	for i, workflow := range workflows {
		want := fmt.Sprintf("workflow-%02d.yaml", i)
		if workflow.filename != want {
			t.Errorf("workflow[%d] filename = %q, want %q", i, workflow.filename, want)
		}
	}
}

func TestListWorkflowsRealFollowsPagination(t *testing.T) {
	t.Parallel()

	files := makeWorkflowFixtureFiles(51)
	server := newWorkflowFixtureServer(t, files, 20)
	defer server.Close()

	client := newWorkflowFixtureClient(t, server)
	workflows, err := listWorkflowsReal(t.Context(), client, "owner", "repo")
	if err != nil {
		t.Fatalf("listWorkflowsReal returned error: %v", err)
	}
	if len(workflows) != len(files) {
		t.Fatalf("got %d workflows, want %d", len(workflows), len(files))
	}
	for i, workflow := range workflows {
		want := fmt.Sprintf("workflow-%02d.yaml", i)
		if workflow.filename != want {
			t.Errorf("workflow[%d] filename = %q, want %q", i, workflow.filename, want)
		}
	}
}

func TestListWorkflowsRealParseFailureIsNotSkipped(t *testing.T) {
	t.Parallel()

	files := makeWorkflowFixtureFiles(51)
	files[25] = "name: broken\non: [push, pull_request]\nthis is not valid yaml\n"
	server := newWorkflowFixtureServer(t, files, 30)
	defer server.Close()

	client := newWorkflowFixtureClient(t, server)
	workflows, err := listWorkflowsReal(t.Context(), client, "owner", "repo")
	if err == nil {
		t.Fatalf("listWorkflowsReal returned %d workflows without an error", len(workflows))
	}
	if workflows != nil {
		t.Errorf("listWorkflowsReal = %v, want nil workflows on parse failure", workflows)
	}
	if !strings.Contains(err.Error(), "workflow-25.yaml") {
		t.Errorf("error %q does not identify the failed workflow", err)
	}
}

func TestListWorkflowsRealMissingDirectory(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	client := newWorkflowFixtureClient(t, server)
	workflows, err := listWorkflowsReal(t.Context(), client, "owner", "repo")
	if err != nil {
		t.Fatalf("listWorkflowsReal returned error for missing directory: %v", err)
	}
	if len(workflows) != 0 {
		t.Errorf("got %d workflows, want none", len(workflows))
	}
}

func makeWorkflowFixtureFiles(count int) []string {
	files := make([]string, count)
	for i := range files {
		files[i] = fmt.Sprintf("name: workflow-%02d\non: [push, pull_request]\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: example/action@v1\n", i)
	}
	return files
}

// newWorkflowFixtureServer serves a workflow directory split into pages of
// pageSize entries. Directory entries are returned in reverse path order to
// verify callers do not rely on API ordering.
func newWorkflowFixtureServer(t *testing.T, files []string, pageSize int) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		path := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/contents/")
		if path == workflowDirectoryPath {
			page, err := strconv.Atoi(r.URL.Query().Get("page"))
			if err != nil {
				page = 1
			}
			if pageSize <= 0 {
				http.NotFound(w, r)
				return
			}
			start := (page - 1) * pageSize
			end := start + pageSize
			if start >= len(files) {
				http.NotFound(w, r)
				return
			}
			if end > len(files) {
				end = len(files)
			}

			contents := make([]*github.RepositoryContent, 0, end-start)
			for i := end - 1; i >= start; i-- {
				contents = append(contents, &github.RepositoryContent{
					Name: github.Ptr(fmt.Sprintf("workflow-%02d.yaml", i)),
					Path: github.Ptr(fmt.Sprintf("%s/workflow-%02d.yaml", workflowDirectoryPath, i)),
					Type: github.Ptr("file"),
				})
			}
			if end < len(files) {
				w.Header().Set("Link", fmt.Sprintf("<%s?page=%d&per_page=%d>; rel=\"next\"",
					r.URL.Path, page+1, workflowDirPageSize))
			}
			if err := json.NewEncoder(w).Encode(contents); err != nil {
				t.Errorf("encoding workflow directory response: %v", err)
			}
			return
		}

		contents, ok := workflowFixtureFileContent(files, path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(&github.RepositoryContent{
			Name:     github.Ptr(strings.TrimPrefix(path, workflowDirectoryPath+"/")),
			Path:     github.Ptr(path),
			Type:     github.Ptr("file"),
			Encoding: github.Ptr("base64"),
			Content:  github.Ptr(base64.StdEncoding.EncodeToString([]byte(contents))),
		}); err != nil {
			t.Errorf("encoding workflow file response: %v", err)
		}
	}))
	return server
}

func workflowFixtureFileContent(files []string, path string) (string, bool) {
	name := strings.TrimPrefix(path, workflowDirectoryPath+"/")
	var index int
	if _, err := fmt.Sscanf(name, "workflow-%d.yaml", &index); err != nil {
		return "", false
	}
	if index < 0 || index >= len(files) {
		return "", false
	}
	return files[index], true
}

func newWorkflowFixtureClient(t *testing.T, server *httptest.Server) *github.Client {
	t.Helper()

	client := github.NewClient(server.Client())
	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatalf("parsing test server URL: %v", err)
	}
	client.BaseURL = baseURL
	return client
}
