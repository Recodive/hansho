package provider

import "testing"

func TestCheckEndpoint(t *testing.T) {
	for endpoint, ok := range map[string]bool{
		"https://hub.example.com": true, "https://hub.example.com/": true, "http://127.0.0.1:8080": true, "http://localhost": true,
		"http://hub.example.com": false, "hub.example.com": false, "ftp://hub.example.com": false, "https://": false, "": false,
		"https://hub.example.com?x=1": false, "https://hub.example.com#f": false,
	} {
		if _, err := checkEndpoint(endpoint, "key"); (err == nil) != ok {
			t.Errorf("checkEndpoint(%q): err = %v, want ok = %v", endpoint, err, ok)
		}
	}
	if base, _ := checkEndpoint("https://hub.example.com/", "key"); base != "https://hub.example.com" {
		t.Errorf("trailing slash kept: %q", base)
	}
	if _, err := checkEndpoint("https://hub.example.com", ""); err == nil {
		t.Error("an empty api key was accepted")
	}
}
