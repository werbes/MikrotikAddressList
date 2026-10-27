package main

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

const maxCountrySelections = 26 * 26

var listNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

type scriptOptions struct {
	ListName        string
	CountryCodes    []string
	ReplaceExisting bool
}

func validateListName(value string) error {
	if !listNamePattern.MatchString(value) {
		return errors.New("address-list name must be 1-64 characters using letters, numbers, dots, underscores, or hyphens")
	}
	return nil
}

func selectCountries(catalog *Catalog, codes []string) ([]Country, error) {
	requested := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		code = strings.ToUpper(strings.TrimSpace(code))
		if code == "" {
			continue
		}
		if !validCountryCode(code) {
			return nil, errors.New("country codes must contain exactly two letters")
		}
		requested[code] = struct{}{}
		if len(requested) > maxCountrySelections {
			return nil, fmt.Errorf("select no more than %d countries", maxCountrySelections)
		}
	}
	if len(requested) == 0 {
		return nil, errors.New("select at least one country")
	}

	selected := make([]Country, 0, len(requested))
	for _, country := range catalog.Countries {
		if _, ok := requested[country.Code]; ok {
			selected = append(selected, country)
			delete(requested, country.Code)
		}
	}
	if len(requested) > 0 {
		unknown := make([]string, 0, len(requested))
		for code := range requested {
			unknown = append(unknown, code)
		}
		sort.Strings(unknown)
		if len(unknown) <= 10 {
			return nil, fmt.Errorf("country code is not available: %s", strings.Join(unknown, ", "))
		}
		return nil, fmt.Errorf("%d country codes are not available; the first is %s", len(unknown), unknown[0])
	}
	return selected, nil
}

func generateMikrotikScript(writer io.Writer, catalog *Catalog, options scriptOptions, selected []Country, now time.Time) error {
	if err := validateListName(options.ListName); err != nil {
		return err
	}
	if len(selected) == 0 {
		return errors.New("select at least one country")
	}

	quotedList := routerOSQuote(options.ListName)
	if _, err := fmt.Fprintf(writer, "# MikroTik country address list: %s\n", options.ListName); err != nil {
		return err
	}
	for _, country := range selected {
		if _, err := fmt.Fprintf(writer, "# Country: %s (%s)\n", sanitizeComment(country.Name), country.Code); err != nil {
			return err
		}
	}
	lines := []string{
		"# IP2Location LITE: https://lite.ip2location.com",
		fmt.Sprintf("# Database version: %s", catalog.DatabaseDate),
		fmt.Sprintf("# Generated: %s", now.UTC().Format(time.RFC3339)),
		"/ip/firewall/address-list",
	}
	if options.ReplaceExisting {
		lines = append(lines, fmt.Sprintf("remove [find where list=%s]", quotedList))
	} else {
		lines = append(lines, "# Existing addresses are retained.")
	}
	for _, line := range lines {
		if _, err := io.WriteString(writer, line+"\n"); err != nil {
			return err
		}
	}

	for _, country := range selected {
		comment := routerOSQuote(sanitizeComment(fmt.Sprintf("%s - %s", country.Code, country.Name)))
		for _, item := range catalog.ranges(country.Code) {
			err := cidrsForRange(item, func(cidr string) error {
				_, err := fmt.Fprintf(writer, "add list=%s address=%s comment=%s\n", quotedList, cidr, comment)
				return err
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func routerOSQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, `$`, `\$`)
	return `"` + value + `"`
}

func sanitizeComment(value string) string {
	return strings.Map(func(value rune) rune {
		if !unicode.IsPrint(value) {
			return ' '
		}
		return value
	}, value)
}
