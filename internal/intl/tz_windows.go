//go:build windows

package intl

import "golang.org/x/sys/windows/registry"

// windowsZones maps the Windows zone names that matter to IANA names (CLDR windowsZones.xml, primary territory).
var windowsZones = map[string]string{
	"UTC": "UTC", "GMT Standard Time": "Europe/London", "W. Europe Standard Time": "Europe/Berlin", "Romance Standard Time": "Europe/Paris",
	"Central Europe Standard Time": "Europe/Budapest", "Central European Standard Time": "Europe/Warsaw", "E. Europe Standard Time": "Europe/Chisinau",
	"GTB Standard Time": "Europe/Bucharest", "FLE Standard Time": "Europe/Kiev", "Russian Standard Time": "Europe/Moscow", "Turkey Standard Time": "Europe/Istanbul",
	"Eastern Standard Time": "America/New_York", "Central Standard Time": "America/Chicago", "Mountain Standard Time": "America/Denver",
	"Pacific Standard Time": "America/Los_Angeles", "US Mountain Standard Time": "America/Phoenix", "Alaskan Standard Time": "America/Anchorage",
	"Hawaiian Standard Time": "Pacific/Honolulu", "Canada Central Standard Time": "America/Regina", "Atlantic Standard Time": "America/Halifax",
	"Newfoundland Standard Time": "America/St_Johns", "E. South America Standard Time": "America/Sao_Paulo", "Argentina Standard Time": "America/Buenos_Aires",
	"SA Pacific Standard Time": "America/Bogota", "Central America Standard Time": "America/Guatemala", "Mexico Standard Time": "America/Mexico_City",
	"India Standard Time": "Asia/Calcutta", "China Standard Time": "Asia/Shanghai", "Tokyo Standard Time": "Asia/Tokyo", "Korea Standard Time": "Asia/Seoul",
	"Singapore Standard Time": "Asia/Singapore", "SE Asia Standard Time": "Asia/Bangkok", "Taipei Standard Time": "Asia/Taipei", "Arab Standard Time": "Asia/Riyadh",
	"Arabian Standard Time": "Asia/Dubai", "Israel Standard Time": "Asia/Jerusalem", "Iran Standard Time": "Asia/Tehran", "Pakistan Standard Time": "Asia/Karachi",
	"Bangladesh Standard Time": "Asia/Dhaka", "Egypt Standard Time": "Africa/Cairo", "South Africa Standard Time": "Africa/Johannesburg",
	"W. Central Africa Standard Time": "Africa/Lagos", "E. Africa Standard Time": "Africa/Nairobi", "AUS Eastern Standard Time": "Australia/Sydney",
	"E. Australia Standard Time": "Australia/Brisbane", "Cen. Australia Standard Time": "Australia/Adelaide", "W. Australia Standard Time": "Australia/Perth",
	"New Zealand Standard Time": "Pacific/Auckland",
}

func windowsZone() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\TimeZoneInformation`, registry.QUERY_VALUE)
	if err == nil {
		defer k.Close()
		if v, _, err := k.GetStringValue("TimeZoneKeyName"); err == nil {
			if n, ok := windowsZones[v]; ok {
				return n
			}
		}
	}
	return "UTC"
}
