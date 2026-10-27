package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
)

const (
	ip2LocationHeaderSize      = 64
	ipv4RecordSize             = 16
	maxDatabaseSize            = 256 << 20
	maxIPv4Records             = 8_000_000
	maxCountryMetadataPointers = 65_536
)

type Country struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	RangeCount   int    `json:"range_count"`
	AddressCount uint64 `json:"address_count"`
}

type ipRange struct {
	Start uint32
	End   uint32
}

type countryState struct {
	code   string
	name   string
	ranges []ipRange
}

type Catalog struct {
	DatabaseDate string
	Countries    []Country
	TotalRanges  int
	SourceBytes  int64

	rangesByCode map[string][]ipRange
	countries    map[string]Country
}

func loadCatalog(path string) (*Catalog, error) {
	// #nosec G304 -- path is built only from the server's configured data directory.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < ip2LocationHeaderSize {
		return nil, errors.New("IP2Location database is smaller than its header")
	}
	if info.Size() > maxDatabaseSize {
		return nil, fmt.Errorf("IP2Location database exceeds the %d byte limit", maxDatabaseSize)
	}

	data, err := io.ReadAll(io.LimitReader(file, maxDatabaseSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDatabaseSize {
		return nil, fmt.Errorf("IP2Location database exceeds the %d byte limit", maxDatabaseSize)
	}
	return parseCatalog(data)
}

func parseCatalog(data []byte) (*Catalog, error) {
	if len(data) < ip2LocationHeaderSize {
		return nil, errors.New("IP2Location database is smaller than its header")
	}

	databaseType := data[0]
	year, month, day := int(data[2]), int(data[3]), int(data[4])
	recordCount := binary.LittleEndian.Uint32(data[5:9])
	ipv4Address := binary.LittleEndian.Uint32(data[9:13])
	productCode := data[29]

	if databaseType == 0 || databaseType > 28 {
		return nil, fmt.Errorf("unsupported IP2Location database type %d", databaseType)
	}
	if year >= 21 && productCode != 1 {
		return nil, errors.New("file is not a valid IP2Location BIN database")
	}
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return nil, errors.New("IP2Location database has an invalid release date")
	}
	if recordCount == 0 {
		return nil, errors.New("IP2Location database has no IPv4 records")
	}
	if recordCount > maxIPv4Records {
		return nil, fmt.Errorf("IP2Location IPv4 record count exceeds the %d record limit", maxIPv4Records)
	}
	if ipv4Address < ip2LocationHeaderSize+1 {
		return nil, errors.New("IP2Location database has an invalid IPv4 data offset")
	}

	recordsStart := uint64(ipv4Address - 1)
	recordsEnd := recordsStart + uint64(recordCount)*ipv4RecordSize
	if recordsEnd > uint64(len(data)) {
		return nil, fmt.Errorf("IP2Location IPv4 record table extends beyond the file")
	}

	states := make(map[string]*countryState)
	countryCache := make(map[uint32]countryState)

	var previousStart uint32
	var previousCode string
	for index := uint32(0); index < recordCount; index++ {
		offset := recordsStart + uint64(index)*ipv4RecordSize
		start := binary.LittleEndian.Uint32(data[offset : offset+4])

		if index > 0 {
			if start <= previousStart {
				return nil, fmt.Errorf("IP2Location IPv4 records are not strictly ordered at index %d", index)
			}
			if previousCode != "" {
				addRange(states, previousCode, previousStart, start-1)
			}
		}

		countryPointer := binary.LittleEndian.Uint32(data[offset+4 : offset+8])
		cached, ok := countryCache[countryPointer]
		if !ok {
			cached = readCountry(data, countryPointer)
			if len(countryCache) < maxCountryMetadataPointers {
				countryCache[countryPointer] = cached
			}
		}
		if cached.code != "" {
			if _, exists := states[cached.code]; !exists {
				states[cached.code] = &countryState{code: cached.code, name: cached.name}
			}
			previousCode = cached.code
		} else {
			previousCode = ""
		}
		previousStart = start
	}
	if previousCode != "" {
		addRange(states, previousCode, previousStart, math.MaxUint32)
	}

	catalog := &Catalog{
		DatabaseDate: fmt.Sprintf("20%02d-%02d-%02d", year, month, day),
		SourceBytes:  int64(len(data)),
		rangesByCode: make(map[string][]ipRange, len(states)),
		countries:    make(map[string]Country, len(states)),
	}

	for code, state := range states {
		if len(state.ranges) == 0 {
			continue
		}
		var addresses uint64
		for _, item := range state.ranges {
			addresses += uint64(item.End) - uint64(item.Start) + 1
		}
		country := Country{
			Code:         code,
			Name:         state.name,
			RangeCount:   len(state.ranges),
			AddressCount: addresses,
		}
		catalog.rangesByCode[code] = state.ranges
		catalog.countries[code] = country
		catalog.Countries = append(catalog.Countries, country)
		catalog.TotalRanges += len(state.ranges)
	}

	if len(catalog.Countries) == 0 {
		return nil, errors.New("IP2Location database contains no country ranges")
	}

	sort.Slice(catalog.Countries, func(i, j int) bool {
		if catalog.Countries[i].Name == catalog.Countries[j].Name {
			return catalog.Countries[i].Code < catalog.Countries[j].Code
		}
		return catalog.Countries[i].Name < catalog.Countries[j].Name
	})

	return catalog, nil
}

func addRange(states map[string]*countryState, code string, start, end uint32) {
	if code == "" || end < start {
		return
	}
	state := states[code]
	if state == nil {
		return
	}
	if count := len(state.ranges); count > 0 {
		last := &state.ranges[count-1]
		if last.End != math.MaxUint32 && last.End+1 == start {
			last.End = end
			return
		}
	}
	state.ranges = append(state.ranges, ipRange{Start: start, End: end})
}

func readCountry(data []byte, pointer uint32) countryState {
	code, next, ok := readLengthPrefixedString(data, uint64(pointer))
	if !ok || !validCountryCode(code) {
		return countryState{}
	}
	name, _, ok := readLengthPrefixedString(data, next)
	if !ok || name == "" {
		name = code
	}
	return countryState{code: code, name: name}
}

func readLengthPrefixedString(data []byte, offset uint64) (string, uint64, bool) {
	if offset >= uint64(len(data)) {
		return "", 0, false
	}
	length := uint64(data[offset])
	start := offset + 1
	end := start + length
	if end < start || end > uint64(len(data)) {
		return "", 0, false
	}
	return decodeLatin1(data[start:end]), end, true
}

func decodeLatin1(data []byte) string {
	var builder strings.Builder
	builder.Grow(len(data))
	for _, value := range data {
		if value < 0x80 {
			builder.WriteByte(value)
		} else {
			builder.WriteRune(rune(value))
		}
	}
	return builder.String()
}

func validCountryCode(code string) bool {
	if len(code) != 2 {
		return false
	}
	return code[0] >= 'A' && code[0] <= 'Z' && code[1] >= 'A' && code[1] <= 'Z'
}

func (c *Catalog) ranges(code string) []ipRange {
	return c.rangesByCode[code]
}

func (c *Catalog) country(code string) (Country, bool) {
	country, ok := c.countries[code]
	return country, ok
}
