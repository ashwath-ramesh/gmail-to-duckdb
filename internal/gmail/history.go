package gmail

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

// historyReply is the part of a users.history.list reply that sync uses.
type historyReply struct {
	History []struct {
		MessagesAdded   []historyMessage `json:"messagesAdded"`
		MessagesDeleted []historyMessage `json:"messagesDeleted"`
		LabelsAdded     []historyMessage `json:"labelsAdded"`
		LabelsRemoved   []historyMessage `json:"labelsRemoved"`
	} `json:"history"`
	HistoryID     uint64 `json:"historyId,string"`
	NextPageToken string `json:"nextPageToken"`
}

type historyMessage struct {
	Message struct {
		ID string `json:"id"`
	} `json:"message"`
}

func historyPage(r historyReply) HistoryPage {
	page := HistoryPage{NextPageToken: r.NextPageToken, HistoryID: r.HistoryID}
	seenAdd := map[string]bool{}
	seenDel := map[string]bool{}
	for _, h := range r.History {
		for _, m := range h.MessagesAdded {
			if id := m.Message.ID; id != "" && !seenAdd[id] {
				seenAdd[id] = true
				page.Added = append(page.Added, id)
			}
		}
		for _, m := range h.MessagesDeleted {
			if id := m.Message.ID; id != "" && !seenDel[id] {
				seenDel[id] = true
				page.Deleted = append(page.Deleted, id)
			}
		}
		for _, list := range [][]historyMessage{h.LabelsAdded, h.LabelsRemoved} {
			for _, m := range list {
				if id := m.Message.ID; id != "" {
					page.LabelChanged = append(page.LabelChanged, id)
				}
			}
		}
	}
	return page
}
