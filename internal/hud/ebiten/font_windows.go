//go:build windows && !nolayershell

package ebitenhud

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"golang.org/x/sys/windows/registry"
)

const windowsFontsRegistryPath = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Fonts`

func loadWindowsFont(family string, bold bool) (*text.GoTextFaceSource, string, error) {
	path, err := windowsFontPath(family, bold)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	source, err := text.NewGoTextFaceSource(bytes.NewReader(data))
	if err == nil {
		return source, path, nil
	}
	collection, collectionErr := text.NewGoTextFaceSourcesFromCollection(bytes.NewReader(data))
	if collectionErr != nil || len(collection) == 0 {
		return nil, "", fmt.Errorf("parse %s: %w", path, err)
	}
	return collection[0], path, nil
}

func windowsFontPath(family string, bold bool) (string, error) {
	type hive struct {
		root registry.Key
		name string
	}
	var errs []error
	for _, candidate := range []hive{
		{registry.CURRENT_USER, "HKCU"},
		{registry.LOCAL_MACHINE, "HKLM"},
	} {
		key, err := registry.OpenKey(candidate.root, windowsFontsRegistryPath, registry.QUERY_VALUE)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", candidate.name, err))
			continue
		}
		path, findErr := fontPathFromRegistry(key, family, bold)
		_ = key.Close()
		if findErr == nil {
			return path, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", candidate.name, findErr))
	}
	return "", errors.Join(errs...)
}

func fontPathFromRegistry(key registry.Key, family string, bold bool) (string, error) {
	names, err := key.ReadValueNames(-1)
	if err != nil {
		return "", err
	}
	family = strings.ToLower(strings.TrimSpace(family))
	var fallback string
	for _, name := range names {
		lower := strings.ToLower(name)
		if !strings.HasPrefix(lower, family) {
			continue
		}
		isBold := strings.Contains(lower, "bold")
		isItalic := strings.Contains(lower, "italic") || strings.Contains(lower, "oblique")
		if isItalic || isBold != bold {
			continue
		}
		value, _, err := key.GetStringValue(name)
		if err != nil {
			continue
		}
		if lower == family+" (truetype)" || lower == family+" (opentype)" {
			return absoluteFontPath(value), nil
		}
		if fallback == "" {
			fallback = value
		}
	}
	if fallback != "" {
		return absoluteFontPath(fallback), nil
	}
	weight := "regular"
	if bold {
		weight = "bold"
	}
	return "", fmt.Errorf("%s %s was not present in installed-font registry entries", family, weight)
}

func absoluteFontPath(value string) string {
	value = os.ExpandEnv(strings.TrimSpace(value))
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(os.Getenv("WINDIR"), "Fonts", value)
}
