package gmail

import (
	"encoding/json"
	"testing"

	gmailapi "google.golang.org/api/gmail/v1"
)

func TestParseHistory(t *testing.T) {
	raw := []byte(`{
		"history": [
			{
				"messagesAdded": [{"message": {"id": "a", "labelIds": ["INBOX"]}}],
				"messagesDeleted": [{"message": {"id": "d"}}],
				"labelsAdded": [{"message": {"id": "x", "labelIds": ["INBOX", "UNREAD"]}}],
				"labelsRemoved": [{"message": {"id": "y", "labelIds": ["INBOX"]}}]
			}
		],
		"historyId": "77",
		"nextPageToken": "n1"
	}`)
	var r gmailapi.ListHistoryResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	page := historyPage(&r)
	if page.HistoryID != 77 || page.NextPageToken != "n1" {
		t.Fatalf("meta: %+v", page)
	}
	if len(page.Added) != 1 || page.Added[0] != "a" {
		t.Fatalf("added: %#v", page.Added)
	}
	if len(page.Deleted) != 1 || page.Deleted[0] != "d" {
		t.Fatalf("deleted: %#v", page.Deleted)
	}
	if len(page.LabelChanged) != 2 || page.LabelChanged[0] != "x" || page.LabelChanged[1] != "y" {
		t.Fatalf("labels: %#v", page.LabelChanged)
	}
}
