package ansible

import (
	"fmt"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestYAMLInventory(t *testing.T) {
	var content map[string]any
	err := yaml.Unmarshal([]byte(`
all:
  vars: {ntp: pool}
  hosts:
    lonely:
  children:
    web:
      vars: {port: 80}
      hosts:
        web[08:10]: {tier: front}
      children:
        edge:
          hosts:
            cdn-[a:b]:
    db:
      hosts:
        db1: {ansible_host: 10.0.0.5}
        web09: {extra: true}
`), &content)
	if err != nil {
		t.Fatal(err)
	}
	d, err := YAMLInventory(content)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(d.Hosts) != "[lonely db1 web09 web08 web10 cdn-a cdn-b]" {
		t.Fatalf("hosts %v", d.Hosts)
	}
	if d.AllVars["ntp"] != "pool" || d.Groups["web"].Vars["port"] != 80 {
		t.Fatalf("vars %v %v", d.AllVars, d.Groups["web"].Vars)
	}
	if fmt.Sprint(d.Groups["web"].Children) != "[edge]" || len(d.Groups["edge"].Hosts) != 2 {
		t.Fatalf("children %+v", d.Groups)
	}
	if hv := d.HostVars["web09"]; hv["tier"] != "front" || hv["extra"] != true {
		t.Fatalf("merged hostvars %v", hv)
	}
	for _, bad := range []string{"all: [a]", "web: {hosts: [a]}", "web: {nodes: {}}", "web: {hosts: {'x[3:1]':}}"} {
		var c map[string]any
		yaml.Unmarshal([]byte(bad), &c)
		if _, err := YAMLInventory(c); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestExpandHostPattern(t *testing.T) {
	cases := map[string]string{
		"plain":          "[plain]",
		"web[1:3]":       "[web1 web2 web3]",
		"web[01:03].lan": "[web01.lan web02.lan web03.lan]",
		"n[0:6:3]":       "[n0 n3 n6]",
		"r[a:b]-[1:2]":   "[ra-1 ra-2 rb-1 rb-2]",
	}
	for in, want := range cases {
		got, err := expandHostPattern(in)
		if err != nil || fmt.Sprint(got) != want {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
}
