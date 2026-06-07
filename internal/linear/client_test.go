package linear

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNormalizeIssueLabels(t *testing.T) {
	raw := rawIssue{
		ID:         "id1",
		Identifier: "MT-1",
		Title:      "Test Issue",
		State:      struct{ Name string `json:"name"` }{Name: "Todo"},
	}
	raw.Labels.Nodes = []struct {
		Name string `json:"name"`
	}{
		{Name: "Bug"},
		{Name: "URGENT"},
		{Name: "frontend"},
	}

	issue := normalizeIssue(raw)

	if len(issue.Labels) != 3 {
		t.Fatalf("expected 3 labels, got %d", len(issue.Labels))
	}
	expected := []string{"bug", "urgent", "frontend"}
	for i, want := range expected {
		if issue.Labels[i] != want {
			t.Errorf("label[%d]: got %q, want %q", i, issue.Labels[i], want)
		}
	}
}

func TestNormalizeIssueBlockers(t *testing.T) {
	raw := rawIssue{
		ID:         "id2",
		Identifier: "MT-2",
		Title:      "Blocked Issue",
		State:      struct{ Name string `json:"name"` }{Name: "Todo"},
	}
	raw.InverseRelations.Nodes = []struct {
		Type  string `json:"type"`
		Issue struct {
			ID         string `json:"id"`
			Identifier string `json:"identifier"`
			State      struct {
				Name string `json:"name"`
			} `json:"state"`
		} `json:"issue"`
	}{
		{
			Type: "blocks",
			Issue: struct {
				ID         string `json:"id"`
				Identifier string `json:"identifier"`
				State      struct {
					Name string `json:"name"`
				} `json:"state"`
			}{
				ID:         "blocker-id",
				Identifier: "MT-1",
				State:      struct{ Name string `json:"name"` }{Name: "In Progress"},
			},
		},
		{
			Type: "relates",
			Issue: struct {
				ID         string `json:"id"`
				Identifier string `json:"identifier"`
				State      struct {
					Name string `json:"name"`
				} `json:"state"`
			}{
				ID:         "related-id",
				Identifier: "MT-3",
				State:      struct{ Name string `json:"name"` }{Name: "Done"},
			},
		},
	}

	issue := normalizeIssue(raw)

	if len(issue.BlockedBy) != 1 {
		t.Fatalf("expected 1 blocker, got %d", len(issue.BlockedBy))
	}
	if *issue.BlockedBy[0].Identifier != "MT-1" {
		t.Errorf("blocker identifier: got %q, want %q", *issue.BlockedBy[0].Identifier, "MT-1")
	}
	if *issue.BlockedBy[0].State != "In Progress" {
		t.Errorf("blocker state: got %q, want %q", *issue.BlockedBy[0].State, "In Progress")
	}
}

func TestNormalizeIssuePriority(t *testing.T) {
	pri := 2
	raw := rawIssue{
		ID:         "id3",
		Identifier: "MT-3",
		Title:      "Priority Test",
		Priority:   &pri,
		State:      struct{ Name string `json:"name"` }{Name: "Todo"},
	}

	issue := normalizeIssue(raw)

	if issue.Priority == nil {
		t.Fatal("expected non-nil priority")
	}
	if *issue.Priority != 2 {
		t.Errorf("priority: got %d, want 2", *issue.Priority)
	}
}

func TestNormalizeIssuePriorityNil(t *testing.T) {
	raw := rawIssue{
		ID:         "id4",
		Identifier: "MT-4",
		Title:      "No Priority",
		State:      struct{ Name string `json:"name"` }{Name: "Todo"},
	}

	issue := normalizeIssue(raw)

	if issue.Priority != nil {
		t.Errorf("expected nil priority, got %d", *issue.Priority)
	}
}

func TestNormalizeIssueTimestamps(t *testing.T) {
	created := "2026-01-15T10:30:00Z"
	updated := "2026-01-16T14:00:00Z"
	raw := rawIssue{
		ID:         "id5",
		Identifier: "MT-5",
		Title:      "Timestamp Test",
		CreatedAt:  &created,
		UpdatedAt:  &updated,
		State:      struct{ Name string `json:"name"` }{Name: "Todo"},
	}

	issue := normalizeIssue(raw)

	if issue.CreatedAt == nil {
		t.Fatal("expected non-nil created_at")
	}
	expectedCreated, _ := time.Parse(time.RFC3339, created)
	if !issue.CreatedAt.Equal(expectedCreated) {
		t.Errorf("created_at: got %v, want %v", issue.CreatedAt, expectedCreated)
	}

	if issue.UpdatedAt == nil {
		t.Fatal("expected non-nil updated_at")
	}
	expectedUpdated, _ := time.Parse(time.RFC3339, updated)
	if !issue.UpdatedAt.Equal(expectedUpdated) {
		t.Errorf("updated_at: got %v, want %v", issue.UpdatedAt, expectedUpdated)
	}
}

func TestNormalizeIssueState(t *testing.T) {
	raw := rawIssue{
		ID:         "id6",
		Identifier: "MT-6",
		Title:      "State Test",
		State:      struct{ Name string `json:"name"` }{Name: "In Progress"},
	}

	issue := normalizeIssue(raw)

	if issue.State != "In Progress" {
		t.Errorf("state: got %q, want %q", issue.State, "In Progress")
	}
}

func TestNormalizeIssueEmptyLabels(t *testing.T) {
	raw := rawIssue{
		ID:         "id7",
		Identifier: "MT-7",
		Title:      "No Labels",
		State:      struct{ Name string `json:"name"` }{Name: "Todo"},
	}

	issue := normalizeIssue(raw)

	if issue.Labels == nil {
		t.Fatal("expected non-nil labels slice")
	}
	if len(issue.Labels) != 0 {
		t.Errorf("expected 0 labels, got %d", len(issue.Labels))
	}
}

func TestIssueJSON(t *testing.T) {
	pri := 1
	now := time.Now().UTC()
	issue := Issue{
		ID:         "id8",
		Identifier: "MT-8",
		Title:      "JSON Test",
		Priority:   &pri,
		State:      "Todo",
		Labels:     []string{"bug", "p1"},
		CreatedAt:  &now,
	}

	data, err := json.Marshal(issue)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded Issue
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.ID != issue.ID {
		t.Errorf("ID mismatch: got %q, want %q", decoded.ID, issue.ID)
	}
	if decoded.Identifier != issue.Identifier {
		t.Errorf("Identifier mismatch: got %q, want %q", decoded.Identifier, issue.Identifier)
	}
	if *decoded.Priority != *issue.Priority {
		t.Errorf("Priority mismatch: got %d, want %d", *decoded.Priority, *issue.Priority)
	}
}

func TestBlockerRefString(t *testing.T) {
	id := "block-1"
	ident := "MT-99"
	state := "In Progress"
	ref := BlockerRef{
		ID:         &id,
		Identifier: &ident,
		State:      &state,
	}

	data, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	var decoded BlockerRef
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if *decoded.Identifier != ident {
		t.Errorf("got %q, want %q", *decoded.Identifier, ident)
	}
}
