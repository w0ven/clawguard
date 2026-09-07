//go:build assistant_web_standalone

package bot

// File-list test support only. These are the exact constants from group_assistant.go.
// This permits testing the unchanged production web file while bot's duplicate
// int64Ptr declaration blocks the complete package. Do not use this tag for ./....
const assistantMaxWebBytes = 2 << 20
const assistantMaxWebRedirects = 3
