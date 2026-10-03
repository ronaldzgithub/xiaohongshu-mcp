package xiaohongshu

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Only the requested note and its current comments snapshot establish readiness.
// Animations, images and analytics are not prerequisites for a data read.
const detailSnapshotJS = `(id) => {
 const entry = window.__INITIAL_STATE__?.note?.noteDetailMap?.[id];
 if (!entry || !entry.note || entry.note.noteId !== id ||
     typeof entry.note.title !== 'string' || typeof entry.note.desc !== 'string' ||
     typeof entry.note.type !== 'string' || !entry.comments ||
     typeof entry.comments !== 'object' || Array.isArray(entry.comments)) return '';
 return JSON.stringify(entry);
}`

func (f *FeedDetailAction) GetFeedDetailSnapshot(ctx context.Context, feedID, token string) (*FeedDetailResponse, error) {
	page := f.page.Context(ctx)
	return readDetailSnapshot(ctx, feedID,
		func() error { return page.Navigate(makeFeedDetailURL(feedID, token)) },
		func() (string, error) {
			value, err := page.Eval(detailSnapshotJS, feedID)
			if err != nil {
				return "", err
			}
			return value.Value.Str(), nil
		})
}

// Callbacks keep deadline, cancellation and exact-object checks testable without
// a browser. No navigation retry and no Must* panic can outlive this request.
func readDetailSnapshot(ctx context.Context, feedID string, navigate func() error, read func() (string, error)) (*FeedDetailResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := navigate(); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := read()
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if raw != "" {
			return decodeDetailSnapshot(raw, feedID)
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func decodeDetailSnapshot(raw, feedID string) (*FeedDetailResponse, error) {
	var entry struct {
		Note struct {
			ID    *string `json:"noteId"`
			Title *string `json:"title"`
			Desc  *string `json:"desc"`
			Type  *string `json:"type"`
		} `json:"note"`
		Comments json.RawMessage `json:"comments"`
	}
	if json.Unmarshal([]byte(raw), &entry) != nil {
		return nil, errors.New("invalid detail snapshot JSON")
	}
	if entry.Note.ID == nil || *entry.Note.ID != feedID || entry.Note.Title == nil || entry.Note.Desc == nil || entry.Note.Type == nil || len(entry.Comments) == 0 || entry.Comments[0] != '{' {
		return nil, errors.New("detail snapshot object or shape mismatch")
	}
	var result FeedDetailResponse
	if json.Unmarshal([]byte(raw), &result) != nil {
		return nil, errors.New("invalid detail snapshot fields")
	}
	return &result, nil
}
