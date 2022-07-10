package chttp

import (
	"encoding/json"
	"html/template"
)

type LivewireComponent interface {
	Name() string
}

type LivewireMessage struct {
	Fingerprint LivewireFingerprint `json:"fingerprint"`
	ServerMemo  LivewireServerMemo  `json:"serverMemo"`
	Updates     []LivewireUpdate    `json:"updates"`
}

type LivewireMessageResponse struct {
	Effects    LivewireEffectsResponse `json:"effects"`
	ServerMemo LivewireServerMemo      `json:"serverMemo"`
}

type LivewireFingerprint struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Locale           string `json:"locale"`
	Path             string `json:"path"`
	Method           string `json:"method"`
	InvalidationHash string `json:"v"`
}

type LivewireEffectsRequest struct {
	Listeners []string `json:"listeners"`
}

type LivewireEffectsResponse struct {
	Dirty []string      `json:"dirty"`
	HTML  template.HTML `json:"html"`
}

type LivewireServerMemo struct {
	HTMLHash string          `json:"htmlHash"`
	Data     json.RawMessage `json:"data"`
	DataMeta []string        `json:"dataMeta"`
	Children []string        `json:"children"`
	Errors   []string        `json:"errors"`
}

type LivewireUpdate struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type LivewireUpdatePayloadCallMethod struct {
	ID     string   `json:"id"`
	Method string   `json:"method"`
	Params []string `json:"params"`
}

type LivewireUpdatePayloadSyncInput struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Value string `json:"value"`
}
