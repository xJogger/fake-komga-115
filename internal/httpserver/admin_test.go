package httpserver

import "testing"

func TestLocalServiceURL(t *testing.T) {
	tests := map[string]string{
		"":          "http://127.0.0.1:25600",
		"0.0.0.0":   "http://127.0.0.1:25600",
		"::":        "http://[::1]:25600",
		"192.0.2.5": "http://192.0.2.5:25600",
	}
	for host, want := range tests {
		if got := localServiceURL(host, 25600); got != want {
			t.Fatalf("localServiceURL(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestIsVirtualInterface(t *testing.T) {
	for _, name := range []string{
		"docker0", "br-1234", "vethabc", "virbr0", "vEthernet (Default Switch)", "WSL",
	} {
		if !isVirtualInterface(name) {
			t.Fatalf("%q should be treated as virtual", name)
		}
	}
	for _, name := range []string{"Ethernet", "Wi-Fi", "enp3s0", "wlan0"} {
		if isVirtualInterface(name) {
			t.Fatalf("%q should not be treated as virtual", name)
		}
	}
}
