package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

var linearGraphQLEndpoint = "https://api.linear.app/graphql"

// LinearClient provides operations against the Linear GraphQL API.
type LinearClient interface {
	// FetchIssuesByLabel returns issues in the given project that carry the specified label name.
	FetchIssuesByLabel(ctx context.Context, projectID, labelName string) ([]Issue, error)

	// GetLabelID returns the ID of the label with the given name within a team.
	GetLabelID(ctx context.Context, teamID, labelName string) (string, error)

	// AddLabel adds labelID to the issue identified by issueID.
	AddLabel(ctx context.Context, issueID, labelID string) error

	// RemoveLabel removes labelID from the issue identified by issueID.
	RemoveLabel(ctx context.Context, issueID, labelID string) error

	// AddComment posts a comment body on the given issue.
	AddComment(ctx context.Context, issueID, body string) error

	// CreateIssue creates a new issue in a team with optional project and label IDs.
	CreateIssue(ctx context.Context, input CreateIssueInput) (*CreatedIssue, error)
}

// httpLinearClient is the production implementation using net/http.
type httpLinearClient struct {
	apiKey     string
	httpClient *http.Client
}

// NewClient returns a LinearClient authenticated with the given API key.
func NewClient(apiKey string) LinearClient {
	return &httpLinearClient{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// FetchIssuesByLabel returns issues in the project that have the given label.
func (c *httpLinearClient) FetchIssuesByLabel(ctx context.Context, projectID, labelName string) ([]Issue, error) {
	const query = `
query($projectId: ID!, $labelName: String!) {
  issues(
    filter: {
      project: { id: { eq: $projectId } }
      labels: { name: { eq: $labelName } }
      state: { type: { neq: "cancelled" } }
    }
    first: 50
  ) {
    nodes {
      id
      identifier
      title
      description
      labels { nodes { id name } }
    }
  }
}`
	var data issuesByProjectData
	if err := c.do(ctx, query, map[string]any{
		"projectId": projectID,
		"labelName": labelName,
	}, &data); err != nil {
		return nil, err
	}
	return data.Issues.Nodes, nil
}

// GetLabelID returns the ID of the label with the given name within a team.
func (c *httpLinearClient) GetLabelID(ctx context.Context, teamID, labelName string) (string, error) {
	const query = `
query($teamId: ID!, $name: String!) {
  issueLabels(filter: { team: { id: { eq: $teamId } }, name: { eq: $name } }) {
    nodes { id name }
  }
}`
	var data labelsData
	if err := c.do(ctx, query, map[string]any{
		"teamId": teamID,
		"name":   labelName,
	}, &data); err != nil {
		return "", err
	}
	if len(data.IssueLabels.Nodes) == 0 {
		return "", fmt.Errorf("linear label %q not found in team %s", labelName, teamID)
	}
	return data.IssueLabels.Nodes[0].ID, nil
}

// AddLabel adds the label to the issue via Linear's atomic label mutation.
func (c *httpLinearClient) AddLabel(ctx context.Context, issueID, labelID string) error {
	const mutation = `
mutation($issueId: String!, $labelId: String!) {
  issueAddLabel(id: $issueId, labelId: $labelId) {
    success
  }
}`
	var data issueAddLabelData
	if err := c.do(ctx, mutation, map[string]any{
		"issueId": issueID,
		"labelId": labelID,
	}, &data); err != nil {
		return err
	}
	if !data.IssueAddLabel.Success {
		return fmt.Errorf("issueAddLabel returned success=false")
	}
	return nil
}

// RemoveLabel removes the label from the issue via Linear's atomic label mutation.
func (c *httpLinearClient) RemoveLabel(ctx context.Context, issueID, labelID string) error {
	const mutation = `
mutation($issueId: String!, $labelId: String!) {
  issueRemoveLabel(id: $issueId, labelId: $labelId) {
    success
  }
}`
	var data issueRemoveLabelData
	if err := c.do(ctx, mutation, map[string]any{
		"issueId": issueID,
		"labelId": labelID,
	}, &data); err != nil {
		return err
	}
	if !data.IssueRemoveLabel.Success {
		return fmt.Errorf("issueRemoveLabel returned success=false")
	}
	return nil
}

// AddComment posts a Markdown comment on the issue.
func (c *httpLinearClient) AddComment(ctx context.Context, issueID, body string) error {
	const mutation = `
mutation($issueId: String!, $body: String!) {
  commentCreate(input: { issueId: $issueId, body: $body }) {
    success
  }
}`
	var data commentCreateData
	if err := c.do(ctx, mutation, map[string]any{
		"issueId": issueID,
		"body":    body,
	}, &data); err != nil {
		return err
	}
	if !data.CommentCreate.Success {
		return fmt.Errorf("commentCreate returned success=false")
	}
	return nil
}

// CreateIssue creates a new issue via the Linear GraphQL API.
func (c *httpLinearClient) CreateIssue(ctx context.Context, input CreateIssueInput) (*CreatedIssue, error) {
	const mutation = `
mutation($teamId: String!, $title: String, $description: String, $projectId: String, $labelIds: [String!]) {
  issueCreate(input: { teamId: $teamId, title: $title, description: $description, projectId: $projectId, labelIds: $labelIds }) {
    success
    issue { id identifier url }
  }
}`
	vars := map[string]any{
		"teamId": input.TeamID,
		"title":  input.Title,
	}
	if input.Description != "" {
		vars["description"] = input.Description
	}
	if input.ProjectID != "" {
		vars["projectId"] = input.ProjectID
	}
	if len(input.LabelIDs) > 0 {
		vars["labelIds"] = input.LabelIDs
	}

	var data issueCreateData
	if err := c.do(ctx, mutation, vars, &data); err != nil {
		return nil, err
	}
	if !data.IssueCreate.Success {
		return nil, fmt.Errorf("issueCreate returned success=false")
	}
	return &CreatedIssue{
		ID:         data.IssueCreate.Issue.ID,
		Identifier: data.IssueCreate.Issue.Identifier,
		URL:        data.IssueCreate.Issue.URL,
	}, nil
}

// currentLabelIDs returns the IDs of labels currently on the issue.
func (c *httpLinearClient) currentLabelIDs(ctx context.Context, issueID string) ([]string, error) {
	const query = `
query($id: String!) {
  issue(id: $id) {
    labels { nodes { id } }
  }
}`
	type issueData struct {
		Issue struct {
			Labels struct {
				Nodes []LabelNode `json:"nodes"`
			} `json:"labels"`
		} `json:"issue"`
	}
	var data issueData
	if err := c.do(ctx, query, map[string]any{"id": issueID}, &data); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(data.Issue.Labels.Nodes))
	for _, n := range data.Issue.Labels.Nodes {
		ids = append(ids, n.ID)
	}
	return ids, nil
}

// do executes a GraphQL request and unmarshals the data field into out.
func (c *httpLinearClient) do(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(graphqlRequest{Query: query, Variables: vars})
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, linearGraphQLEndpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("linear API request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("linear API returned HTTP %d: %s", resp.StatusCode, raw)
	}

	// Unmarshal into a typed envelope where Data is our target type.
	type envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []graphqlError  `json:"errors,omitempty"`
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("unmarshaling response: %w", err)
	}
	if len(env.Errors) > 0 {
		return fmt.Errorf("linear GraphQL error: %s", env.Errors[0].Message)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("unmarshaling data: %w", err)
	}
	return nil
}
