package gmail

import "testing"

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
	page, err := parseHistory(raw)
	if err != nil {
		t.Fatal(err)
	}
	if page.HistoryID != 77 || page.NextPageToken != "n1" {
		t.Fatalf("meta: %+v", page)
	}
	if len(page.Added) != 1 || page.Added[0] != "a" {
		t.Fatalf("added: %#v", page.Added)
	}
	if len(page.Deleted) != 1 || page.Deleted[0] != "d" {
		t.Fatalf("deleted: %#v", page.Deleted)
	}
	if len(page.LabelUpdates) != 2 {
		t.Fatalf("labels: %#v", page.LabelUpdates)
	}
	if page.LabelUpdates[0].ID != "x" || len(page.LabelUpdates[0].Labels) != 2 {
		t.Fatalf("x: %+v", page.LabelUpdates[0])
	}
	if page.LabelUpdates[1].ID != "y" || len(page.LabelUpdates[1].Labels) != 1 {
		t.Fatalf("y: %+v", page.LabelUpdates[1])
	}
}
