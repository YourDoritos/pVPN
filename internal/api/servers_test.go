package api

import (
	"testing"
)

func testServers() []LogicalServer {
	return []LogicalServer{
		{Name: "CH#1", ExitCountry: "CH", City: "Zurich", Load: 30, Tier: 2, Status: 1, Features: ServerFeatureP2P},
		{Name: "CH#2", ExitCountry: "CH", City: "Geneva", Load: 60, Tier: 2, Status: 1, Features: 0},
		{Name: "US#1", ExitCountry: "US", City: "New York", Load: 10, Tier: 2, Status: 1, Features: ServerFeatureStreaming},
		{Name: "US#2", ExitCountry: "US", City: "Los Angeles", Load: 80, Tier: 0, Status: 1, Features: 0},
		{Name: "DE#1", ExitCountry: "DE", City: "Berlin", Load: 20, Tier: 2, Status: 1, Features: ServerFeatureP2P | ServerFeatureStreaming},
		{Name: "SE#1", ExitCountry: "SE", City: "Stockholm", Load: 15, Tier: 2, Status: 1, Features: ServerFeatureSecureCore, EntryCountry: "CH"},
		{Name: "JP#1", ExitCountry: "JP", City: "Tokyo", Load: 50, Tier: 2, Status: 0, Features: 0},
		{Name: "FR#1", ExitCountry: "FR", City: "Paris", Load: 40, Tier: 2, Status: 1, Features: ServerFeatureTor},
		{Name: "US-FREE#1", ExitCountry: "US", City: "New York", Load: 90, Tier: 0, Status: 1, Features: 0},
	}
}

func TestFilterServers_OnlineOnly(t *testing.T) {
	result := FilterServers(testServers(), ServerFilter{OnlineOnly: true}, 2)
	for _, s := range result {
		if !s.IsOnline() {
			t.Errorf("offline server %s not filtered", s.Name)
		}
	}
}

func TestFilterServers_Country(t *testing.T) {
	result := FilterServers(testServers(), ServerFilter{Country: "CH"}, 2)
	if len(result) != 2 {
		t.Errorf("expected 2 CH servers, got %d", len(result))
	}
	for _, s := range result {
		if s.ExitCountry != "CH" {
			t.Errorf("got non-CH server %s", s.Name)
		}
	}
}

func TestFilterServers_TierFiltering(t *testing.T) {
	// Free user should only see free servers
	result := FilterServers(testServers(), ServerFilter{}, 0)
	for _, s := range result {
		if s.EffectiveTier() > 0 {
			t.Errorf("free user sees tier %d server %s", s.Tier, s.Name)
		}
	}
	// Plus user sees all
	result = FilterServers(testServers(), ServerFilter{}, 2)
	if len(result) != len(testServers()) {
		t.Errorf("plus user: expected %d, got %d", len(testServers()), len(result))
	}
}

func TestFilterServers_FeatureFilters(t *testing.T) {
	tests := []struct {
		name   string
		filter ServerFilter
		want   int
	}{
		{"P2P", ServerFilter{P2P: true}, 2},
		{"Tor", ServerFilter{Tor: true}, 1},
		{"Streaming", ServerFilter{Streaming: true}, 2},
		{"SecureCore", ServerFilter{SecureCore: true}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FilterServers(testServers(), tt.filter, 2)
			if len(result) != tt.want {
				t.Errorf("expected %d, got %d", tt.want, len(result))
			}
		})
	}
}

func TestFilterServers_ExcludeName(t *testing.T) {
	result := FilterServers(testServers(), ServerFilter{ExcludeName: "CH#1"}, 2)
	for _, s := range result {
		if s.Name == "CH#1" {
			t.Error("CH#1 should be excluded")
		}
	}
}

func TestFilterServers_SearchQuery(t *testing.T) {
	tests := []struct {
		query   string
		wantMin int
	}{
		{"CH", 2},
		{"york", 2},
		{"DE#1", 1},
		{"zzzzz", 0},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := FilterServers(testServers(), ServerFilter{SearchQuery: tt.query}, 2)
			if len(result) < tt.wantMin {
				t.Errorf("query %q: want >=%d, got %d", tt.query, tt.wantMin, len(result))
			}
		})
	}
}

func TestFilterServers_Combined(t *testing.T) {
	result := FilterServers(testServers(), ServerFilter{
		OnlineOnly: true, Country: "US", Streaming: true,
	}, 2)
	if len(result) != 1 || result[0].Name != "US#1" {
		t.Errorf("expected US#1 only, got %v", result)
	}
}

func TestFilterServers_Empty(t *testing.T) {
	if len(FilterServers(nil, ServerFilter{}, 2)) != 0 {
		t.Error("expected empty for nil input")
	}
}

func TestFindFastestServer(t *testing.T) {
	s := FindFastestServer(testServers(), ServerFilter{OnlineOnly: true}, 2)
	if s == nil {
		t.Fatal("got nil")
	}
	if s.Name != "US#1" {
		t.Errorf("expected US#1 (load=10), got %s (load=%d)", s.Name, s.Load)
	}
}

func TestFindFastestServer_NoMatch(t *testing.T) {
	if FindFastestServer(testServers(), ServerFilter{Country: "XX"}, 2) != nil {
		t.Error("expected nil for no match")
	}
}

func TestFindServerByName(t *testing.T) {
	servers := testServers()
	if s := FindServerByName(servers, "DE#1"); s == nil || s.Name != "DE#1" {
		t.Errorf("expected DE#1, got %v", s)
	}
	if s := FindServerByName(servers, "de#1"); s == nil {
		t.Error("expected case-insensitive match")
	}
	if FindServerByName(servers, "XX#99") != nil {
		t.Error("expected nil for non-existent")
	}
}

func TestGroupServersByCountry(t *testing.T) {
	groups := GroupServersByCountry(testServers())
	if len(groups["CH"]) != 2 {
		t.Errorf("expected 2 CH, got %d", len(groups["CH"]))
	}
	if len(groups["US"]) != 3 {
		t.Errorf("expected 3 US, got %d", len(groups["US"]))
	}
}

func TestCountryList(t *testing.T) {
	countries := CountryList(testServers())
	if len(countries) != 6 {
		t.Errorf("expected 6 countries, got %d: %v", len(countries), countries)
	}
	for i := 1; i < len(countries); i++ {
		if countries[i] < countries[i-1] {
			t.Error("not sorted")
		}
	}
}
