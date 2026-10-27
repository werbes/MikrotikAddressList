package main

import (
	"encoding/binary"
	"strings"
	"testing"
)

func testDatabase(t *testing.T) []byte {
	t.Helper()

	const (
		recordCount = 4
		dataStart   = ip2LocationHeaderSize
	)
	data := make([]byte, dataStart+recordCount*ipv4RecordSize)

	data[0] = 3
	data[1] = 4
	data[2] = 26
	data[3] = 10
	data[4] = 15
	binary.LittleEndian.PutUint32(data[5:9], recordCount)
	binary.LittleEndian.PutUint32(data[9:13], dataStart+1)
	data[29] = 1

	appendCountry := func(code, name string) uint32 {
		pointer := uint32(len(data))
		data = append(data, byte(len(code)))
		data = append(data, code...)
		data = append(data, byte(len(name)))
		data = append(data, name...)
		return pointer
	}
	australia := appendCountry("AU", "Australia")
	unitedStates := appendCountry("US", "United States")

	writeRecord := func(index int, start uint32, countryPointer uint32) {
		offset := dataStart + index*ipv4RecordSize
		binary.LittleEndian.PutUint32(data[offset:offset+4], start)
		binary.LittleEndian.PutUint32(data[offset+4:offset+8], countryPointer)
	}
	writeRecord(0, 0, 0)
	writeRecord(1, 0x01000000, australia)
	writeRecord(2, 0x01000100, australia)
	writeRecord(3, 0x01000200, unitedStates)

	binary.LittleEndian.PutUint32(data[31:35], uint32(len(data)))
	return data
}

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	catalog, err := parseCatalog(testDatabase(t))
	if err != nil {
		t.Fatalf("parseCatalog() error = %v", err)
	}
	return catalog
}

func TestParseCatalog(t *testing.T) {
	catalog := testCatalog(t)

	if catalog.DatabaseDate != "2026-10-15" {
		t.Fatalf("DatabaseDate = %q, want 2026-10-15", catalog.DatabaseDate)
	}
	if len(catalog.Countries) != 2 {
		t.Fatalf("len(Countries) = %d, want 2", len(catalog.Countries))
	}
	if catalog.TotalRanges != 2 {
		t.Fatalf("TotalRanges = %d, want 2 merged ranges", catalog.TotalRanges)
	}

	australia, ok := catalog.country("AU")
	if !ok {
		t.Fatal("AU country was not parsed")
	}
	if australia.Name != "Australia" {
		t.Fatalf("AU name = %q", australia.Name)
	}
	if australia.RangeCount != 1 {
		t.Fatalf("AU RangeCount = %d, want 1 after merging", australia.RangeCount)
	}
	ranges := catalog.ranges("AU")
	if len(ranges) != 1 || ranges[0] != (ipRange{Start: 0x01000000, End: 0x010001FF}) {
		t.Fatalf("AU ranges = %#v", ranges)
	}

	unitedStates, ok := catalog.country("US")
	if !ok {
		t.Fatal("US country was not parsed")
	}
	if unitedStates.AddressCount != uint64(0xFFFFFFFF-0x01000200+1) {
		t.Fatalf("US AddressCount = %d", unitedStates.AddressCount)
	}
}

func TestParseCatalogRejectsTruncatedRecords(t *testing.T) {
	data := testDatabase(t)
	data = data[:len(data)-1]
	data = data[:ip2LocationHeaderSize+2*ipv4RecordSize]
	if _, err := parseCatalog(data); err == nil {
		t.Fatal("parseCatalog() error = nil for truncated record table")
	}
}

func TestParseCatalogRejectsExcessiveRecordCount(t *testing.T) {
	data := testDatabase(t)
	binary.LittleEndian.PutUint32(data[5:9], maxIPv4Records+1)

	if _, err := parseCatalog(data); err == nil || !strings.Contains(err.Error(), "record limit") {
		t.Fatalf("parseCatalog() error = %v, want record limit error", err)
	}
}

func TestDecodeLatin1(t *testing.T) {
	got := decodeLatin1([]byte{'C', 0xf4, 't', 'e'})
	if got != "Côte" {
		t.Fatalf("decodeLatin1() = %q, want Côte", got)
	}
}
