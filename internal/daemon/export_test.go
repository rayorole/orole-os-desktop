package daemon

import "github.com/rayorole/orole-os-desktop/internal/credentials"

// UseMemoryLLMCredentials makes daemons created afterwards keep provider keys and the ntfy topic and
// token in memory, so tests never read or write the user's real Credential Manager entries. It returns
// the restore function.
func UseMemoryLLMCredentials() (restore func()) {
	prev, prevNtfy := llmCredentialStore, ntfyCredentialStore
	mem := credentials.NewMemory()
	llmCredentialStore = func() credentials.Store { return mem }
	ntfyCredentialStore = func() credentials.Store { return mem }
	return func() { llmCredentialStore, ntfyCredentialStore = prev, prevNtfy }
}
