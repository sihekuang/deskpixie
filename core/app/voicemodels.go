package app

// The voice model lists (#477): each provider's models, loaded from its API in the
// background into <app data>/voice/models.json, fill the Settings model choices.

import (
	"path/filepath"

	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

// setupModels opens the model cache and its Refresher; a list that changed tells the Settings
// window (settings.changed), which then asks for a snapshot.
func (a *App) setupModels() {
	catalog := a.deps.ModelCatalog
	if catalog == nil {
		catalog = a.catalog
	}
	a.models = &voice.Refresher{
		Cache:    voice.NewModelCache(filepath.Join(a.paths.AppSupport, "voice")),
		Secrets:  a.deps.Secrets,
		Catalog:  catalog,
		OnChange: func(string) { a.emit("settings.changed", nil) },
	}
}

// catalog is the registered adapter's model list for provider, nil when it has none.
func (a *App) catalog(provider string) voice.ModelCatalog {
	f, ok := a.deps.Providers[provider]
	if !ok {
		return nil
	}
	c, _ := f().(voice.ModelCatalog)
	return c
}

// refreshModels loads the current provider's list unless it is fresh. Never called under the
// settings API lock (it reads the config and the keys).
func (a *App) refreshModels() { a.models.Refresh(a.store.Current().Voice.Provider) }

// modelSources adds the loaded lists and why each didn't load to src.
func (a *App) modelSources(src *settings.OptionSources) {
	src.VoiceModels, src.VoiceModelsNote = map[string][]string{}, map[string]string{}
	for _, p := range ProviderNames() {
		l := a.models.Cache.Get(p)
		if len(l.Models) > 0 {
			src.VoiceModels[p] = l.Models
		}
		if l.Error != "" {
			src.VoiceModelsNote[p] = l.Error
		}
	}
}

// keysChanged loads again the lists of the providers whose key before differs from now.
func (a *App) keysChanged(before map[string]voice.Secret) {
	for p, old := range before {
		if now, _ := a.deps.Secrets.APIKey(p); now != old {
			a.models.RefreshNow(p)
		}
	}
}

// keysNow is each provider's key (the zero Secret for none), to compare after a change.
func (a *App) keysNow() map[string]voice.Secret {
	out := map[string]voice.Secret{}
	for _, p := range ProviderNames() {
		out[p], _ = a.deps.Secrets.APIKey(p)
	}
	return out
}
