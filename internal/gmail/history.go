package gmail

import (
	"encoding/json"
	"strconv"
)

type HistoryPage struct {
	Added         []string
	Deleted       []string
	LabelUpdates  []LabelUpdate
	HistoryID     uint64
	NextPageToken string
}

type LabelUpdate struct {
	ID     string
	Labels []string
}

type historyResponse struct {
	History       []historyRecord `json:"history"`
	NextPageToken string          `json:"nextPageToken"`
	HistoryID     string          `json:"historyId"`
}

type historyRecord struct {
	MessagesAdded   []historyMsg `json:"messagesAdded"`
	MessagesDeleted []historyMsg `json:"messagesDeleted"`
	LabelsAdded     []historyMsg `json:"labelsAdded"`
	LabelsRemoved   []historyMsg `json:"labelsRemoved"`
}

type historyMsg struct {
	Message struct {
		ID       string   `json:"id"`
		LabelIDs []string `json:"labelIds"`
	} `json:"message"`
}

func parseHistory(raw []byte) (HistoryPage, error) {
	var r historyResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return HistoryPage{}, err
	}
	page := HistoryPage{NextPageToken: r.NextPageToken}
	if hid, err := strconv.ParseUint(r.HistoryID, 10, 64); err == nil {
		page.HistoryID = hid
	}
	seenAdd := map[string]struct{}{}
	seenDel := map[string]struct{}{}
	for _, rec := range r.History {
		for _, m := range rec.MessagesAdded {
			id := m.Message.ID
			if id == "" {
				continue
			}
			if _, ok := seenAdd[id]; ok {
				continue
			}
			seenAdd[id] = struct{}{}
			page.Added = append(page.Added, id)
		}
		for _, m := range rec.MessagesDeleted {
			id := m.Message.ID
			if id == "" {
				continue
			}
			if _, ok := seenDel[id]; ok {
				continue
			}
			seenDel[id] = struct{}{}
			page.Deleted = append(page.Deleted, id)
		}
		for _, m := range rec.LabelsAdded {
			if m.Message.ID == "" {
				continue
			}
			page.LabelUpdates = append(page.LabelUpdates, LabelUpdate{
				ID:     m.Message.ID,
				Labels: m.Message.LabelIDs,
			})
		}
		for _, m := range rec.LabelsRemoved {
			if m.Message.ID == "" {
				continue
			}
			page.LabelUpdates = append(page.LabelUpdates, LabelUpdate{
				ID:     m.Message.ID,
				Labels: m.Message.LabelIDs,
			})
		}
	}
	return page, nil
}
