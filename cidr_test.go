package main

import (
	"reflect"
	"testing"
)

func collectCIDRs(t *testing.T, item ipRange) []string {
	t.Helper()
	var result []string
	if err := cidrsForRange(item, func(cidr string) error {
		result = append(result, cidr)
		return nil
	}); err != nil {
		t.Fatalf("cidrsForRange() error = %v", err)
	}
	return result
}

func TestCIDRsForRange(t *testing.T) {
	tests := []struct {
		name string
		item ipRange
		want []string
	}{
		{
			name: "single address",
			item: ipRange{Start: 0x01000001, End: 0x01000001},
			want: []string{"1.0.0.1/32"},
		},
		{
			name: "aligned block",
			item: ipRange{Start: 0x01000000, End: 0x010000FF},
			want: []string{"1.0.0.0/24"},
		},
		{
			name: "mixed blocks",
			item: ipRange{Start: 0x01000001, End: 0x01000006},
			want: []string{"1.0.0.1/32", "1.0.0.2/31", "1.0.0.4/31", "1.0.0.6/32"},
		},
		{
			name: "entire IPv4 space",
			item: ipRange{Start: 0, End: 0xFFFFFFFF},
			want: []string{"0.0.0.0/0"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := collectCIDRs(t, test.item)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("CIDRs = %#v, want %#v", got, test.want)
			}
		})
	}
}
