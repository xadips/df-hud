package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixtureNow(t *testing.T, m *BossMap) time.Time {
	t.Helper()
	return m.FetchedAt
}

func loadFixtureBossMap(t *testing.T) *BossMap {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "bossmap.json"))
	if err != nil {
		t.Fatal(err)
	}
	var top struct {
		ServerTime int64 `json:"servertime"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	m, err := parseBossMap(data, time.Unix(top.ServerTime, 0))
	if err != nil {
		t.Fatal(err)
	}
	return m
}
