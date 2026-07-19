package linear

// Issue represents a Linear issue returned by the GraphQL API.
type Issue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"` // Human-readable ID, e.g. "ENG-123".
	Title       string `json:"title"`
	Description string `json:"description"`
	Labels      struct {
		Nodes []LabelNode `json:"nodes"`
	} `json:"labels"`
}

// LabelNode is a label attached to a Linear issue or belonging to a team.
type LabelNode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// graphqlRequest is the body sent to the Linear GraphQL endpoint.
type graphqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type graphqlError struct {
	Message string `json:"message"`
}

// issuesByProjectData is the data shape for FetchIssuesByLabel.
type issuesByProjectData struct {
	Issues struct {
		Nodes []Issue `json:"nodes"`
	} `json:"issues"`
}

// labelsData is the data shape for GetLabelID.
type labelsData struct {
	IssueLabels struct {
		Nodes []LabelNode `json:"nodes"`
	} `json:"issueLabels"`
}

// issueAddLabelData is the data shape for AddLabel.
type issueAddLabelData struct {
	IssueAddLabel struct {
		Success bool `json:"success"`
	} `json:"issueAddLabel"`
}

// issueRemoveLabelData is the data shape for RemoveLabel.
type issueRemoveLabelData struct {
	IssueRemoveLabel struct {
		Success bool `json:"success"`
	} `json:"issueRemoveLabel"`
}

// commentCreateData is the data shape for AddComment.
type commentCreateData struct {
	CommentCreate struct {
		Success bool `json:"success"`
	} `json:"commentCreate"`
}

// CreateIssueInput is the input for creating a Linear issue.
type CreateIssueInput struct {
	TeamID      string   // Required: the team to create the issue in.
	Title       string   // Required: issue title.
	Description string   // Optional: markdown description.
	ProjectID   string   // Optional: project to add the issue to.
	LabelIDs    []string // Optional: label IDs to attach.
}

// CreatedIssue is the result of creating a Linear issue.
type CreatedIssue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"` // Human-readable ID, e.g. "ENG-123".
	URL        string `json:"url"`
}

// issueCreateData is the data shape for CreateIssue.
type issueCreateData struct {
	IssueCreate struct {
		Success bool `json:"success"`
		Issue   struct {
			ID         string `json:"id"`
			Identifier string `json:"identifier"`
			URL        string `json:"url"`
		} `json:"issue"`
	} `json:"issueCreate"`
}
