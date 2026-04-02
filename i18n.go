// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"embed"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed lang/*.yaml
var langFS embed.FS

// Translations holds a flat map of dot-separated keys to translated strings.
// Example: "checks.nginx_running" -> "running (pid %d)"
type Translations struct {
	strings map[string]string
	lang    string
}

// LoadTranslations loads a translation file for the given language code.
// Falls back to English if the requested language is not found.
func LoadTranslations(lang string) (*Translations, error) {
	t := &Translations{
		strings: make(map[string]string),
		lang:    lang,
	}

	// Always load English as base
	if err := t.loadFile("en"); err != nil {
		return nil, fmt.Errorf("loading base English translations: %w", err)
	}

	// Overlay requested language on top
	if lang != "en" {
		if err := t.loadFile(lang); err != nil {
			return nil, fmt.Errorf("loading %s translations: %w", lang, err)
		}
	}

	return t, nil
}

func (t *Translations) loadFile(lang string) error {
	data, err := langFS.ReadFile("lang/" + lang + ".yaml")
	if err != nil {
		return err
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}

	t.flatten("", raw)
	return nil
}

func (t *Translations) flatten(prefix string, m map[string]any) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch val := v.(type) {
		case map[string]any:
			t.flatten(key, val)
		case string:
			t.strings[key] = val
		}
	}
}

// T returns the translated string for the given key.
// If args are provided, they are passed to fmt.Sprintf.
func (t *Translations) T(key string, args ...any) string {
	s, ok := t.strings[key]
	if !ok {
		return key
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

// Section returns all keys under a given prefix as a flat map.
// Useful for sending a section of translations to the frontend.
// Example: Section("dashboard") returns {"title": "Server-Stat", ...}
func (t *Translations) Section(prefix string) map[string]string {
	result := make(map[string]string)
	pfx := prefix + "."
	for k, v := range t.strings {
		if strings.HasPrefix(k, pfx) {
			result[strings.TrimPrefix(k, pfx)] = v
		}
	}
	return result
}

// Lang returns the current language code.
func (t *Translations) Lang() string {
	return t.lang
}
