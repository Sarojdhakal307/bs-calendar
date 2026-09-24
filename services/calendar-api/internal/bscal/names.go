package bscal

import "strings"

// Localized is a bilingual label.
type Localized struct {
	En string `json:"en"`
	Ne string `json:"ne"`
}

var bsMonths = [12]Localized{
	{"Baisakh", "बैशाख"}, {"Jestha", "जेठ"}, {"Asar", "असार"}, {"Shrawan", "साउन"},
	{"Bhadra", "भदौ"}, {"Ashwin", "असोज"}, {"Kartik", "कात्तिक"}, {"Mangsir", "मंसिर"},
	{"Poush", "पुस"}, {"Magh", "माघ"}, {"Falgun", "फागुन"}, {"Chaitra", "चैत"},
}

var adMonths = [12]Localized{
	{"January", "जनवरी"}, {"February", "फेब्रुअरी"}, {"March", "मार्च"}, {"April", "अप्रिल"},
	{"May", "मे"}, {"June", "जुन"}, {"July", "जुलाई"}, {"August", "अगस्ट"},
	{"September", "सेप्टेम्बर"}, {"October", "अक्टोबर"}, {"November", "नोभेम्बर"}, {"December", "डिसेम्बर"},
}

var weekdays = [7]Localized{
	{"Sunday", "आइतबार"}, {"Monday", "सोमबार"}, {"Tuesday", "मंगलबार"}, {"Wednesday", "बुधबार"},
	{"Thursday", "बिहीबार"}, {"Friday", "शुक्रबार"}, {"Saturday", "शनिबार"},
}

// MonthName returns the bilingual name of a month (1..12) in the given calendar.
func MonthName(cal Calendar, month int) Localized {
	if month < 1 || month > 12 {
		return Localized{}
	}
	if cal == BS {
		return bsMonths[month-1]
	}
	return adMonths[month-1]
}

// WeekdayName returns the bilingual name of a weekday (0 = Sunday).
func WeekdayName(w int) Localized {
	if w < 0 || w > 6 {
		return Localized{}
	}
	return weekdays[w]
}

const devanagariZero = '०'

// ToNepaliDigits replaces ASCII digits with Devanagari digits.
func ToNepaliDigits(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 3)
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(devanagariZero + (r - '0'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// FromNepaliDigits replaces Devanagari digits with ASCII digits.
func FromNepaliDigits(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r >= devanagariZero && r <= devanagariZero+9 }) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if r >= devanagariZero && r <= devanagariZero+9 {
			b.WriteRune('0' + (r - devanagariZero))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
