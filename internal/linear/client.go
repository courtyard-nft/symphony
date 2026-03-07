// Package linear implements the Linear GraphQL API client for fetching and
// normalizing issues. It supports paginated candidate issue queries, issue state
// refresh by ID, and terminal state queries for startup cleanup.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Issue represents a normalized Linear issue per spec s4.1.1.
type Issue struct {
	ID          string      `json:"id"`
	Identifier  string      `json:"identifier"`
	Title       string      `json:"title"`
	Description *string     `json:"description"`
	Priority    *int        `json:"priority"`
	State       string      `json:"state"`
	BranchName  *string     `json:"branch_name"`
	URL         *string     `json:"url"`
	Labels      []string    `json:"labels"`
	BlockedBy   []BlockerRef `json:"blocked_by"`
	CreatedAt   *time.Time  `json:"created_at"`
	UpdatedAt   *time.Time  `json:"updated_at"`
}

// BlockerRef represents a blocking issue reference.
type BlockerRef struct {
	ID         *string `json:"id"`
	Identifier *string `json:"identifier"`
	State      *string `json:"state"`
}

// IssueStateInfo is a minimal issue record for reconciliation.
type IssueStateInfo struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// Client is a Linear GraphQL API client.
type Client struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
	logger     *slog.Logger
	pageSize   int
}

// NewClient creates a new Linear client.
func NewClient(endpoint, apiKey string, logger *slog.Logger) *Client {
	return &Client{
		endpoint: endpoint,
		apiKey:   apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger:   logger,
		pageSize: 50,
	}
}

// UpdateConfig updates the client's endpoint and API key.
func (c *Client) UpdateConfig(endpoint, apiKey string) {
	c.endpoint = endpoint
	c.apiKey = apiKey
}

// graphqlRequest executes a GraphQL query.
func (c *Client) graphqlRequest(ctx context.Context, query string, variables map[string]interface{}) (json.RawMessage, error) {
	body := map[string]interface{}{
		"query":     query,
		"variables": variables,
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("linear_api_request: failed to marshal query: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("linear_api_request: failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("linear_api_request: transport error: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("linear_api_request: failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("linear_api_status: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var gqlResp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &gqlResp); err != nil {
		return nil, fmt.Errorf("linear_unknown_payload: failed to parse response: %w", err)
	}
	if len(gqlResp.Errors) > 0 {
		msgs := make([]string, len(gqlResp.Errors))
		for i, e := range gqlResp.Errors {
			msgs[i] = e.Message
		}
		return nil, fmt.Errorf("linear_graphql_errors: %s", strings.Join(msgs, "; "))
	}

	return gqlResp.Data, nil
}

// candidateIssuesQuery is the GraphQL query for fetching candidate issues.
const candidateIssuesQuery = `
query CandidateIssues($projectSlug: String!, $stateNames: [String!]!, $first: Int!, $after: String) {
  issues(
    filter: {
      project: { slugId: { eq: $projectSlug } }
      state: { name: { in: $stateNames } }
    }
    first: $first
    after: $after
    orderBy: createdAt
  ) {
    pageInfo {
      hasNextPage
      endCursor
    }
    nodes {
      id
      identifier
      title
      description
      priority
      url
      branchName
      createdAt
      updatedAt
      state {
        name
      }
      labels {
        nodes {
          name
        }
      }
      relations(first: 100) {
        nodes {
          type
          relatedIssue {
            id
            identifier
            state {
              name
            }
          }
        }
      }
      inverseRelations(first: 100) {
        nodes {
          type
          issue {
            id
            identifier
            state {
              name
            }
          }
        }
      }
    }
  }
}
`

// FetchCandidateIssues fetches all candidate issues in active states for a project slug.
func (c *Client) FetchCandidateIssues(ctx context.Context, projectSlug string, activeStates []string) ([]Issue, error) {
	var allIssues []Issue
	var cursor *string

	for {
		variables := map[string]interface{}{
			"projectSlug": projectSlug,
			"stateNames":  activeStates,
			"first":       c.pageSize,
		}
		if cursor != nil {
			variables["after"] = *cursor
		}

		data, err := c.graphqlRequest(ctx, candidateIssuesQuery, variables)
		if err != nil {
			return nil, err
		}

		var result struct {
			Issues struct {
				PageInfo struct {
					HasNextPage bool    `json:"hasNextPage"`
					EndCursor   *string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []rawIssue `json:"nodes"`
			} `json:"issues"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("linear_unknown_payload: failed to parse issues: %w", err)
		}

		for _, raw := range result.Issues.Nodes {
			allIssues = append(allIssues, normalizeIssue(raw))
		}

		if !result.Issues.PageInfo.HasNextPage {
			break
		}
		if result.Issues.PageInfo.EndCursor == nil {
			return nil, fmt.Errorf("linear_missing_end_cursor: pagination integrity error")
		}
		cursor = result.Issues.PageInfo.EndCursor
	}

	c.logger.Info("fetched candidate issues", "count", len(allIssues))
	return allIssues, nil
}

// issuesByStatesQuery is the GraphQL query for fetching issues by state names.
const issuesByStatesQuery = `
query IssuesByStates($projectSlug: String!, $stateNames: [String!]!, $first: Int!, $after: String) {
  issues(
    filter: {
      project: { slugId: { eq: $projectSlug } }
      state: { name: { in: $stateNames } }
    }
    first: $first
    after: $after
  ) {
    pageInfo {
      hasNextPage
      endCursor
    }
    nodes {
      id
      identifier
      state {
        name
      }
    }
  }
}
`

// FetchIssuesByStates fetches issues in the given states (used for terminal cleanup).
func (c *Client) FetchIssuesByStates(ctx context.Context, projectSlug string, stateNames []string) ([]IssueStateInfo, error) {
	if len(stateNames) == 0 {
		return nil, nil
	}

	var allIssues []IssueStateInfo
	var cursor *string

	for {
		variables := map[string]interface{}{
			"projectSlug": projectSlug,
			"stateNames":  stateNames,
			"first":       c.pageSize,
		}
		if cursor != nil {
			variables["after"] = *cursor
		}

		data, err := c.graphqlRequest(ctx, issuesByStatesQuery, variables)
		if err != nil {
			return nil, err
		}

		var result struct {
			Issues struct {
				PageInfo struct {
					HasNextPage bool    `json:"hasNextPage"`
					EndCursor   *string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					ID         string `json:"id"`
					Identifier string `json:"identifier"`
					State      struct {
						Name string `json:"name"`
					} `json:"state"`
				} `json:"nodes"`
			} `json:"issues"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("linear_unknown_payload: failed to parse issues by states: %w", err)
		}

		for _, node := range result.Issues.Nodes {
			allIssues = append(allIssues, IssueStateInfo{
				ID:    node.ID,
				State: node.State.Name,
			})
		}

		if !result.Issues.PageInfo.HasNextPage {
			break
		}
		if result.Issues.PageInfo.EndCursor == nil {
			return nil, fmt.Errorf("linear_missing_end_cursor: pagination integrity error")
		}
		cursor = result.Issues.PageInfo.EndCursor
	}

	return allIssues, nil
}

// issueStatesByIDsQuery is the GraphQL query for fetching issue states by IDs.
const issueStatesByIDsQuery = `
query IssueStatesByIDs($ids: [ID!]!) {
  nodes(ids: $ids) {
    ... on Issue {
      id
      identifier
      title
      description
      priority
      url
      branchName
      createdAt
      updatedAt
      state {
        name
      }
      labels {
        nodes {
          name
        }
      }
      relations(first: 100) {
        nodes {
          type
          relatedIssue {
            id
            identifier
            state {
              name
            }
          }
        }
      }
      inverseRelations(first: 100) {
        nodes {
          type
          issue {
            id
            identifier
            state {
              name
            }
          }
        }
      }
    }
  }
}
`

// FetchIssueStatesByIDs fetches current states for the given issue IDs (reconciliation).
func (c *Client) FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]Issue, error) {
	if len(issueIDs) == 0 {
		return nil, nil
	}

	variables := map[string]interface{}{
		"ids": issueIDs,
	}

	data, err := c.graphqlRequest(ctx, issueStatesByIDsQuery, variables)
	if err != nil {
		return nil, err
	}

	var result struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("linear_unknown_payload: failed to parse nodes: %w", err)
	}

	var issues []Issue
	for _, raw := range result.Nodes {
		var ri rawIssue
		if err := json.Unmarshal(raw, &ri); err != nil {
			continue
		}
		if ri.ID == "" {
			continue
		}
		issues = append(issues, normalizeIssue(ri))
	}

	return issues, nil
}

// ExecuteGraphQL executes a raw GraphQL query (for the linear_graphql tool extension).
func (c *Client) ExecuteGraphQL(ctx context.Context, query string, variables map[string]interface{}) (json.RawMessage, error) {
	body := map[string]interface{}{
		"query": query,
	}
	if variables != nil {
		body["variables"] = variables
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal query: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("transport error: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// --- Internal types and normalization ---

type rawIssue struct {
	ID          string  `json:"id"`
	Identifier  string  `json:"identifier"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	Priority    *int    `json:"priority"`
	URL         *string `json:"url"`
	BranchName  *string `json:"branchName"`
	CreatedAt   *string `json:"createdAt"`
	UpdatedAt   *string `json:"updatedAt"`
	State       struct {
		Name string `json:"name"`
	} `json:"state"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Relations struct {
		Nodes []struct {
			Type         string `json:"type"`
			RelatedIssue struct {
				ID         string `json:"id"`
				Identifier string `json:"identifier"`
				State      struct {
					Name string `json:"name"`
				} `json:"state"`
			} `json:"relatedIssue"`
		} `json:"nodes"`
	} `json:"relations"`
	InverseRelations struct {
		Nodes []struct {
			Type  string `json:"type"`
			Issue struct {
				ID         string `json:"id"`
				Identifier string `json:"identifier"`
				State      struct {
					Name string `json:"name"`
				} `json:"state"`
			} `json:"issue"`
		} `json:"nodes"`
	} `json:"inverseRelations"`
}

func normalizeIssue(raw rawIssue) Issue {
	// Normalize labels to lowercase
	labels := make([]string, 0, len(raw.Labels.Nodes))
	for _, l := range raw.Labels.Nodes {
		labels = append(labels, strings.ToLower(l.Name))
	}

	// Extract blockers from inverse relations where type is "blocks"
	var blockedBy []BlockerRef
	for _, rel := range raw.InverseRelations.Nodes {
		if strings.ToLower(rel.Type) == "blocks" {
			id := rel.Issue.ID
			identifier := rel.Issue.Identifier
			state := rel.Issue.State.Name
			blockedBy = append(blockedBy, BlockerRef{
				ID:         strPtr(id),
				Identifier: strPtr(identifier),
				State:      strPtr(state),
			})
		}
	}

	// Parse timestamps
	var createdAt, updatedAt *time.Time
	if raw.CreatedAt != nil {
		if t, err := time.Parse(time.RFC3339, *raw.CreatedAt); err == nil {
			createdAt = &t
		}
	}
	if raw.UpdatedAt != nil {
		if t, err := time.Parse(time.RFC3339, *raw.UpdatedAt); err == nil {
			updatedAt = &t
		}
	}

	return Issue{
		ID:          raw.ID,
		Identifier:  raw.Identifier,
		Title:       raw.Title,
		Description: raw.Description,
		Priority:    raw.Priority,
		State:       raw.State.Name,
		BranchName:  raw.BranchName,
		URL:         raw.URL,
		Labels:      labels,
		BlockedBy:   blockedBy,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
