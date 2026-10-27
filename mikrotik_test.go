package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestGenerateMikrotikScript(t *testing.T) {
	catalog := testCatalog(t)
	selected, err := selectCountries(catalog, []string{"au"})
	if err != nil {
		t.Fatalf("selectCountries() error = %v", err)
	}

	var output bytes.Buffer
	err = generateMikrotikScript(&output, catalog, scriptOptions{
		ListName:        "countries-blocked",
		CountryCodes:    []string{"AU"},
		ReplaceExisting: true,
	}, selected, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("generateMikrotikScript() error = %v", err)
	}

	script := output.String()
	for _, expected := range []string{
		"# MikroTik country address list: countries-blocked",
		"# Country: Australia (AU)",
		"# IP2Location LITE: https://lite.ip2location.com",
		"# Database version: 2026-10-15",
		`remove [find where list="countries-blocked"]`,
		`add list="countries-blocked" address=1.0.0.0/23 comment="AU - Australia"`,
	} {
		if !strings.Contains(script, expected) {
			t.Errorf("script does not contain %q:\n%s", expected, script)
		}
	}
}

func TestGenerateMikrotikScriptKeepsExistingEntries(t *testing.T) {
	catalog := testCatalog(t)
	selected, err := selectCountries(catalog, []string{"AU"})
	if err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	err = generateMikrotikScript(&output, catalog, scriptOptions{
		ListName:        "country_list",
		ReplaceExisting: false,
	}, selected, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "remove [find") {
		t.Fatal("script removes existing entries when replacement is disabled")
	}
	if !strings.Contains(output.String(), "# Existing addresses are retained.") {
		t.Fatal("script does not explain that existing entries are retained")
	}
}

func TestListNameValidation(t *testing.T) {
	valid := []string{"countries-blocked", "allowed_1", "block.eu"}
	for _, value := range valid {
		if err := validateListName(value); err != nil {
			t.Errorf("validateListName(%q) error = %v", value, err)
		}
	}

	invalid := []string{"", "-starts-with-hyphen", `bad"name`, "contains space", strings.Repeat("a", 65)}
	for _, value := range invalid {
		if err := validateListName(value); err == nil {
			t.Errorf("validateListName(%q) error = nil", value)
		}
	}
}

func TestRouterOSQuoteEscapesExpansionAndCommandText(t *testing.T) {
	got := routerOSQuote("ACME \"$5\"; /system reboot")
	want := `"ACME \"\$5\"; /system reboot"`
	if got != want {
		t.Fatalf("routerOSQuote() = %q, want %q", got, want)
	}
}

func TestSanitizeCommentReplacesControlCharacters(t *testing.T) {
	got := sanitizeComment("safe\r\nname\x00with\tcontrols")
	if got != "safe  name with controls" {
		t.Fatalf("sanitizeComment() = %q", got)
	}
}

func TestSelectCountriesRejectsMalformedCodes(t *testing.T) {
	catalog := testCatalog(t)
	for _, code := range []string{"A", "AUS", "A1", `AU"; /system reboot`} {
		if _, err := selectCountries(catalog, []string{code}); err == nil {
			t.Errorf("selectCountries(%q) error = nil", code)
		}
	}
}
