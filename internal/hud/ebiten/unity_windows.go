//go:build windows && !nolayershell

package ebitenhud

import "golang.org/x/sys/windows/registry"

const unityRegistryPath = `Software\Creaky Corpse\Dead Frontier`

type unityDisplayConfig struct {
	Width      int
	Height     int
	Fullscreen int
	Monitor    int
}

func readUnityDisplayConfig() unityDisplayConfig {
	key, err := registry.OpenKey(registry.CURRENT_USER, unityRegistryPath, registry.QUERY_VALUE)
	if err != nil {
		return unityDisplayConfig{}
	}
	defer key.Close()
	read := func(name string) int {
		value, _, err := key.GetIntegerValue(name)
		if err != nil {
			return 0
		}
		return int(value)
	}
	return unityDisplayConfig{
		Width:      read("Screenmanager Resolution Width_h182942802"),
		Height:     read("Screenmanager Resolution Height_h2627697771"),
		Fullscreen: read("Screenmanager Is Fullscreen mode_h3981298716"),
		Monitor:    read("UnitySelectMonitor_h17969598"),
	}
}
