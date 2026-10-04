package main

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

// ExtensionNamespace is the Record Version extension holding the X post
// metadata. It keeps the name of the former built-in kind, so stored Records
// and retrieval mappings carry over.
const ExtensionNamespace = "connector.x_list"

// Post is an X API v2 post object with the fields the connector requests.
type Post struct {
	ID               string         `json:"id"`
	Text             string         `json:"text"`
	CreatedAt        string         `json:"created_at,omitempty"`
	AuthorID         string         `json:"author_id,omitempty"`
	Lang             string         `json:"lang,omitempty"`
	ConversationID   string         `json:"conversation_id,omitempty"`
	EditHistory      []string       `json:"edit_history_tweet_ids,omitempty"`
	Entities         map[string]any `json:"entities,omitempty"`
	ReferencedTweets []Reference    `json:"referenced_tweets,omitempty"`
	Attachments      *Attachments   `json:"attachments,omitempty"`
	NoteTweet        *NoteTweet     `json:"note_tweet,omitempty"`
}

// Reference is a referenced post (quoted, replied_to or retweeted).
type Reference struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Attachments lists a post's media keys.
type Attachments struct {
	MediaKeys []string `json:"media_keys,omitempty"`
}

// NoteTweet carries the full text of a long post.
type NoteTweet struct {
	Text string `json:"text"`
}

// Includes are the expanded users and media of a response.
type Includes struct {
	Users []User  `json:"users,omitempty"`
	Media []Media `json:"media,omitempty"`
}

// User is an expanded post author.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

// Media is an expanded media reference; media bytes are never downloaded.
type Media struct {
	MediaKey        string `json:"media_key"`
	Type            string `json:"type"`
	URL             string `json:"url,omitempty"`
	PreviewImageURL string `json:"preview_image_url,omitempty"`
	AltText         string `json:"alt_text,omitempty"`
}

// Scope is where the instance writes: Relation targets bind to it.
type Scope struct {
	CorpusID  string
	Namespace string
}

var relationTypes = map[string]string{"quoted": "quotes", "replied_to": "replies_to", "retweeted": "reposts"}

// validText mirrors the engine's text rule: valid UTF-8 without NUL.
func validText(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }

// MapPost maps one X post (any version) to a connector item. The Record Key
// is the original post id (first of the edit history), so an edit becomes a
// correction of the same Record; the latest version id is both the revision
// and the Source Position, so an older version never supersedes a newer one.
// It is pure so polling and webhook deliveries share it. Relation targets are
// Records of the instance's own Corpus and Source Namespace.
func MapPost(post Post, inc Includes, scope Scope) quivrplugin.Item {
	history := post.EditHistory
	if len(history) == 0 {
		history = []string{post.ID}
	}
	root := history[0]
	text := post.Text
	if post.NoteTweet != nil && post.NoteTweet.Text != "" {
		text = post.NoteTweet.Text
	}
	if strings.TrimSpace(text) == "" || !validText(text) {
		text = "https://x.com/i/web/status/" + post.ID
	}
	data := map[string]any{"post_id": post.ID, "edit_history_post_ids": history}
	link := "https://x.com/i/web/status/" + post.ID
	for _, u := range inc.Users {
		if u.ID == post.AuthorID && post.AuthorID != "" {
			data["author"] = map[string]any{"id": u.ID, "username": u.Username, "name": u.Name}
			if u.Username != "" {
				link = "https://x.com/" + u.Username + "/status/" + post.ID
			}
		}
	}
	if _, ok := data["author"]; !ok && post.AuthorID != "" {
		data["author"] = map[string]any{"id": post.AuthorID}
	}
	data["url"] = link
	for k, v := range map[string]string{"created_at": post.CreatedAt, "lang": post.Lang, "conversation_id": post.ConversationID} {
		if v != "" {
			data[k] = v
		}
	}
	if len(post.Entities) > 0 {
		data["entities"] = post.Entities
	}
	content := quivrplugin.NewManifest(quivrplugin.TextPart("body", "body", text))
	if len(post.ReferencedTweets) > 0 {
		refs := make([]any, 0, len(post.ReferencedTweets))
		for _, r := range post.ReferencedTweets {
			refs = append(refs, map[string]any{"type": r.Type, "id": r.ID})
			if t, ok := relationTypes[r.Type]; ok && r.ID != "" {
				content.Relations = append(content.Relations, quivrplugin.Relation{Type: t,
					Target: quivrplugin.RelationKey{CorpusID: scope.CorpusID, Namespace: scope.Namespace, RecordKey: r.ID}})
			}
		}
		data["referenced_posts"] = refs
	}
	if post.Attachments != nil && len(post.Attachments.MediaKeys) > 0 {
		media := make([]any, 0, len(post.Attachments.MediaKeys))
		for _, key := range post.Attachments.MediaKeys {
			m := map[string]any{"media_key": key}
			for _, im := range inc.Media {
				if im.MediaKey == key {
					for k, v := range map[string]string{"type": im.Type, "url": im.URL, "preview_image_url": im.PreviewImageURL, "alt_text": im.AltText} {
						if v != "" {
							m[k] = v
						}
					}
				}
			}
			media = append(media, m)
		}
		data["media"] = media
	}
	// A round trip through JSON gives the extension plain JSON values.
	raw, _ := json.Marshal(data)
	var plain map[string]any
	_ = json.Unmarshal(raw, &plain)
	return quivrplugin.Item{
		RecordKey:      root,
		Revision:       post.ID,
		SourcePosition: post.ID,
		Content:        content,
		Extensions:     map[string]quivrplugin.Extension{ExtensionNamespace: {SchemaVersion: "1", Data: plain}},
	}
}
