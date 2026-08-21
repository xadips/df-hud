package hotkeys

import "testing"

func TestParseBindingCanonicalisesSupportedKeys(t *testing.T) {
	cases := map[string]string{
		"v":                   "V",
		"control + shift + m": "Ctrl+Shift+M",
		"Alt+f12":             "Alt+F12",
		"super+backtick":      "Win+Grave",
		"pgdn":                "PageDown",
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			got, err := ParseBinding(input)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != want {
				t.Errorf("canonical binding = %q, want %q", got.String(), want)
			}
		})
	}
}

func TestParseBindingRejectsAmbiguousOrUnsupportedInput(t *testing.T) {
	for _, input := range []string{
		"",
		"Ctrl",
		"Ctrl++M",
		"Ctrl+Ctrl+M",
		"M+N",
		"Mouse4",
		"F25",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := ParseBinding(input); err == nil {
				t.Fatalf("ParseBinding(%q) succeeded", input)
			}
		})
	}
}
