package gmail

import gmailapi "google.golang.org/api/gmail/v1"

// HistoryPage is one page of mailbox changes since a history id.
type HistoryPage struct {
	Added   []string
	Deleted []string
	// LabelChanged lists messages whose labels changed. Sync fetches their
	// metadata again, so the new label list itself is not kept.
	LabelChanged  []string
	HistoryID     uint64
	NextPageToken string
}

func historyPage(r *gmailapi.ListHistoryResponse) HistoryPage {
	page := HistoryPage{NextPageToken: r.NextPageToken, HistoryID: r.HistoryId}
	seenAdd := map[string]bool{}
	seenDel := map[string]bool{}
	for _, h := range r.History {
		if h == nil {
			continue
		}
		for _, m := range h.MessagesAdded {
			if m == nil {
				continue
			}
			if id := msgID(m.Message); id != "" && !seenAdd[id] {
				seenAdd[id] = true
				page.Added = append(page.Added, id)
			}
		}
		for _, m := range h.MessagesDeleted {
			if m == nil {
				continue
			}
			if id := msgID(m.Message); id != "" && !seenDel[id] {
				seenDel[id] = true
				page.Deleted = append(page.Deleted, id)
			}
		}
		for _, m := range h.LabelsAdded {
			if m == nil {
				continue
			}
			if id := msgID(m.Message); id != "" {
				page.LabelChanged = append(page.LabelChanged, id)
			}
		}
		for _, m := range h.LabelsRemoved {
			if m == nil {
				continue
			}
			if id := msgID(m.Message); id != "" {
				page.LabelChanged = append(page.LabelChanged, id)
			}
		}
	}
	return page
}

func msgID(m *gmailapi.Message) string {
	if m == nil {
		return ""
	}
	return m.Id
}
