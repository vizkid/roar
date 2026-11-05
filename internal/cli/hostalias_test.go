package cli

import "testing"

func TestStoredHostAliases(t *testing.T) {
	hosts := []rexHost{{Label: "stiletto-rex.tail4f2cce.ts.net", Endpoints: []string{"https://stiletto-rex.tail4f2cce.ts.net"}}, {Label: "Baroni", Endpoints: []string{"https://baroni-rex.tail4f2cce.ts.net"}}}
	for _, name := range []string{"stiletto", "Stiletto", "stiletto-rex", "stiletto-rex.tail4f2cce.ts.net"} {
		h, ok, err := storedHost(hosts, name)
		if err != nil || !ok || h.Label != hosts[0].Label || h.Endpoints[0] != hosts[0].Endpoints[0] {
			t.Fatalf("%s: %+v %v %v", name, h, ok, err)
		}
	}
	if _, ok, err := storedHost(hosts, "missing"); ok || err != nil {
		t.Fatalf("missing: %v %v", ok, err)
	}
	hosts = append(hosts, rexHost{Label: "another", Endpoints: []string{"https://stiletto-rex.other.ts.net"}})
	if _, _, err := storedHost(hosts, "stiletto"); err == nil {
		t.Fatal("ambiguous alias accepted")
	}
	hosts = append(hosts, rexHost{Label: "stiletto", Endpoints: []string{"https://exact.example"}})
	if h, ok, err := storedHost(hosts, "stiletto"); err != nil || !ok || h.Label != "stiletto" {
		t.Fatalf("exact label lost: %+v %v %v", h, ok, err)
	}
}
