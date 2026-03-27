package tui

import (
	"testing"
)

func TestCountryFlag(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		// Valid 2-letter codes produce regional indicator symbols
		{"US", "\U0001F1FA\U0001F1F8"},
		{"CH", "\U0001F1E8\U0001F1ED"},
		{"DE", "\U0001F1E9\U0001F1EA"},
		{"JP", "\U0001F1EF\U0001F1F5"},
		// Lowercase should also work (function uppercases)
		{"us", "\U0001F1FA\U0001F1F8"},
		{"de", "\U0001F1E9\U0001F1EA"},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			got := countryFlag(tt.code)
			if got != tt.want {
				t.Errorf("countryFlag(%q) = %q, want %q", tt.code, got, tt.want)
			}
		})
	}
}

func TestCountryFlag_Invalid(t *testing.T) {
	tests := []struct {
		name string
		code string
	}{
		{"empty", ""},
		{"single char", "U"},
		{"three chars", "USA"},
		{"four chars", "USAA"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countryFlag(tt.code)
			if got != "  " {
				t.Errorf("countryFlag(%q) = %q, want %q (two spaces)", tt.code, got, "  ")
			}
		})
	}
}

func TestCountryFlag_AllLetters(t *testing.T) {
	// Verify the formula works for boundary letters A and Z
	flagA := countryFlag("AA")
	if len([]rune(flagA)) != 2 {
		t.Errorf("countryFlag(AA) should produce 2 runes, got %d", len([]rune(flagA)))
	}

	flagZ := countryFlag("ZZ")
	if len([]rune(flagZ)) != 2 {
		t.Errorf("countryFlag(ZZ) should produce 2 runes, got %d", len([]rune(flagZ)))
	}
}

func TestCountryName_Known(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		{"US", "United States"},
		{"CH", "Switzerland"},
		{"DE", "Germany"},
		{"JP", "Japan"},
		{"GB", "United Kingdom"},
		{"FR", "France"},
		{"AU", "Australia"},
		{"BR", "Brazil"},
		{"NL", "Netherlands"},
		{"SE", "Sweden"},
		{"ZA", "South Africa"},
		{"KR", "South Korea"},
		{"HK", "Hong Kong"},
		{"NZ", "New Zealand"},
		{"CZ", "Czech Republic"},
		{"AE", "United Arab Emirates"},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			got := countryName(tt.code)
			if got != tt.want {
				t.Errorf("countryName(%q) = %q, want %q", tt.code, got, tt.want)
			}
		})
	}
}

func TestCountryName_CaseInsensitive(t *testing.T) {
	// The function uppercases the code before lookup
	got := countryName("us")
	if got != "United States" {
		t.Errorf("countryName(\"us\") = %q, want %q", got, "United States")
	}

	got = countryName("ch")
	if got != "Switzerland" {
		t.Errorf("countryName(\"ch\") = %q, want %q", got, "Switzerland")
	}
}

func TestCountryName_Unknown(t *testing.T) {
	tests := []struct {
		code string
	}{
		{"XX"},
		{"ZZ"},
		{"QQ"},
		{""},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			got := countryName(tt.code)
			// Unknown codes should return the code itself
			if got != tt.code {
				t.Errorf("countryName(%q) = %q, want %q (code returned as-is)", tt.code, got, tt.code)
			}
		})
	}
}

func TestCountryName_AllMappedEntries(t *testing.T) {
	// Verify a broad set of codes from the map all return non-empty names
	codes := []string{
		"AD", "AE", "AL", "AM", "AR", "AT", "AU", "BA", "BE", "BG",
		"BR", "CA", "CH", "CL", "CO", "CR", "CY", "CZ", "DE", "DK",
		"EE", "EG", "ES", "FI", "FR", "GB", "GE", "GR", "HK", "HR",
		"HU", "ID", "IE", "IL", "IN", "IS", "IT", "JP", "KH", "KR",
		"KZ", "LT", "LU", "LV", "MD", "MK", "MX", "MY", "NG", "NL",
		"NO", "NZ", "PA", "PE", "PH", "PK", "PL", "PR", "PT", "RO",
		"RS", "SE", "SG", "SI", "SK", "TH", "TR", "TW", "UA", "US",
		"VN", "ZA",
	}

	for _, code := range codes {
		name := countryName(code)
		if name == code {
			t.Errorf("countryName(%q) returned code itself, expected a mapped name", code)
		}
		if name == "" {
			t.Errorf("countryName(%q) returned empty string", code)
		}
	}
}
