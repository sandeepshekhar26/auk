package templating

import (
	crand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	mrand "math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// registerRandom adds the "dynamic variable" palette: Postman/Insomnia-style
// faker template functions (${randomEmail}, ${randomInt}, ${randomUuid}, …),
// plus the two bare Postman timestamps (${timestamp}, ${isoTimestamp}) that
// exist for the same import-parity reason.
//
// These exist primarily so an imported Postman collection that uses
// `{{$randomEmail}}` keeps working: a mechanical `$random` -> `random`
// rename turns `{{$randomEmail}}` into AUK's `${randomEmail}` grammar, and
// every name below matches Postman's dynamic-variable name (minus the `$`)
// so that rename resolves cleanly. Postman's dynamic variables never take
// arguments, so each function is usable bare (`${randomInt}`); AUK adds
// optional arguments on a few of them (`${randomInt(1,100)}`) using the same
// call syntax the rest of the engine already parses.
//
// Randomness is sourced fresh on every call (crypto/rand-backed, with a
// math/rand/v2 fallback), so two `${randomInt}` in one request differ. There
// is no global seed or per-run cache to honour — each `${...}` evaluation is
// independent — so nothing here is memoised.
func (e *Engine) registerRandom() {
	// --- Identifiers -------------------------------------------------------
	uuidFn := func([]string) (string, error) { return uuid.NewString(), nil }
	e.funcs["randomUuid"] = uuidFn // task/AUK spelling
	e.funcs["randomUUID"] = uuidFn // Postman: $randomUUID
	e.funcs["guid"] = uuidFn       // Postman: $guid

	// --- Timestamps (Postman's bare $timestamp / $isoTimestamp) ------------
	//
	// AUK's own spellings are the dotted `${timestamp.unix}` and
	// `${timestamp.iso8601}` (registered in registerBuiltins). Postman's two
	// most-used dynamic variables after $guid are the BARE `{{$timestamp}}`
	// and `{{$isoTimestamp}}`, and the importer's `$`-stripping rewrite lands
	// them on `${timestamp}` / `${isoTimestamp}` — names that matched neither
	// a variable nor a function, so every imported request carrying
	// `X-Request-Time: {{$timestamp}}` failed to send with
	// `unresolved variable "timestamp"`. Registering them here is what makes
	// that import work, and it belongs in this file rather than with the
	// dotted builtins because these two exist for the same reason the ~50
	// faker names below do: Postman parity.
	//
	// A user-defined variable named `timestamp` still wins — eval checks
	// variables before bare functions — so this cannot shadow anyone's data.
	e.funcs["timestamp"] = func([]string) (string, error) {
		return strconv.FormatInt(time.Now().Unix(), 10), nil
	}
	e.funcs["isoTimestamp"] = func([]string) (string, error) {
		// Millisecond precision + `Z`, matching Postman's $isoTimestamp
		// (JavaScript's Date.toISOString()) exactly rather than AUK's
		// second-precision ${timestamp.iso8601}. Still valid RFC 3339, which
		// permits a fractional part.
		return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"), nil
	}

	// --- Numbers, text, booleans ------------------------------------------
	e.funcs["randomInt"] = func(args []string) (string, error) {
		lo, hi := 0, 1000 // Postman's default $randomInt range
		switch len(args) {
		case 0:
		case 2:
			var err error
			if lo, err = strconv.Atoi(strings.TrimSpace(args[0])); err != nil {
				return "", fmt.Errorf("randomInt: invalid min %q: %w", args[0], err)
			}
			if hi, err = strconv.Atoi(strings.TrimSpace(args[1])); err != nil {
				return "", fmt.Errorf("randomInt: invalid max %q: %w", args[1], err)
			}
		default:
			return "", fmt.Errorf("randomInt takes no args (default 0..1000) or exactly 2 (min,max)")
		}
		return strconv.Itoa(randRange(lo, hi)), nil
	}

	e.funcs["randomAlphaNumeric"] = func(args []string) (string, error) {
		n := 1 // Postman's $randomAlphaNumeric yields a single character
		if len(args) >= 1 && strings.TrimSpace(args[0]) != "" {
			var err error
			if n, err = strconv.Atoi(strings.TrimSpace(args[0])); err != nil {
				return "", fmt.Errorf("randomAlphaNumeric: invalid length %q: %w", args[0], err)
			}
			if n < 0 {
				return "", fmt.Errorf("randomAlphaNumeric: length must be >= 0")
			}
		}
		return randAlnum(n), nil
	}

	e.funcs["randomBoolean"] = func([]string) (string, error) {
		if randN(2) == 0 {
			return "false", nil
		}
		return "true", nil
	}

	e.funcs["randomColor"] = func([]string) (string, error) { return pick(colorNames), nil }
	e.funcs["randomHexColor"] = func([]string) (string, error) { return "#" + randHex(3), nil }

	e.funcs["randomPrice"] = func(args []string) (string, error) {
		lo, hi := 0.0, 1000.0
		switch len(args) {
		case 0:
		case 2:
			var err error
			if lo, err = strconv.ParseFloat(strings.TrimSpace(args[0]), 64); err != nil {
				return "", fmt.Errorf("randomPrice: invalid min %q: %w", args[0], err)
			}
			if hi, err = strconv.ParseFloat(strings.TrimSpace(args[1]), 64); err != nil {
				return "", fmt.Errorf("randomPrice: invalid max %q: %w", args[1], err)
			}
		default:
			return "", fmt.Errorf("randomPrice takes no args (default 0..1000) or exactly 2 (min,max)")
		}
		if hi < lo {
			lo, hi = hi, lo
		}
		loC, hiC := int(lo*100), int(hi*100)
		cents := loC + randN(hiC-loC+1)
		return strconv.FormatFloat(float64(cents)/100.0, 'f', 2, 64), nil
	}

	// --- Words, lorem ------------------------------------------------------
	e.funcs["randomWord"] = func([]string) (string, error) { return pick(words), nil }
	e.funcs["randomWords"] = func([]string) (string, error) {
		n := randRange(2, 5)
		parts := make([]string, n)
		for i := range parts {
			parts[i] = pick(words)
		}
		return strings.Join(parts, " "), nil
	}
	e.funcs["randomLoremWord"] = func([]string) (string, error) { return pick(loremWords), nil }
	e.funcs["randomLoremWords"] = func([]string) (string, error) { return loremWordsN(randRange(2, 5)), nil }
	e.funcs["randomLoremSentence"] = func([]string) (string, error) { return loremSentence(), nil }
	e.funcs["randomLoremParagraph"] = func([]string) (string, error) {
		n := randRange(3, 5)
		sentences := make([]string, n)
		for i := range sentences {
			sentences[i] = loremSentence()
		}
		return strings.Join(sentences, " "), nil
	}

	// --- People ------------------------------------------------------------
	e.funcs["randomFirstName"] = func([]string) (string, error) { return pick(firstNames), nil }
	e.funcs["randomLastName"] = func([]string) (string, error) { return pick(lastNames), nil }
	e.funcs["randomFullName"] = func([]string) (string, error) {
		return pick(firstNames) + " " + pick(lastNames), nil
	}
	e.funcs["randomNamePrefix"] = func([]string) (string, error) { return pick(namePrefixes), nil }
	e.funcs["randomNameSuffix"] = func([]string) (string, error) { return pick(nameSuffixes), nil }
	e.funcs["randomJobTitle"] = func([]string) (string, error) {
		return pick(jobDescriptors) + " " + pick(jobAreas) + " " + pick(jobTypes), nil
	}

	// --- Internet, domains, emails ----------------------------------------
	emailFn := func([]string) (string, error) {
		local := strings.ToLower(pick(firstNames)) + "." + strings.ToLower(pick(lastNames))
		if randN(2) == 0 {
			local += strconv.Itoa(randRange(1, 99))
		}
		return local + "@" + pick(emailDomains), nil
	}
	e.funcs["randomEmail"] = emailFn
	e.funcs["randomExampleEmail"] = emailFn // Postman: $randomExampleEmail

	e.funcs["randomUserName"] = func([]string) (string, error) {
		u := pick(firstNames)
		switch randN(3) {
		case 0:
			u += "_" + pick(lastNames)
		case 1:
			u += "." + pick(lastNames)
		default:
			u += strconv.Itoa(randRange(1, 999))
		}
		return u, nil
	}

	e.funcs["randomUrl"] = func([]string) (string, error) {
		return "https://" + strings.ToLower(pick(words)) + "." + pick(tlds), nil
	}
	e.funcs["randomDomainName"] = func([]string) (string, error) {
		return strings.ToLower(pick(words)) + "." + pick(tlds), nil
	}
	e.funcs["randomDomainWord"] = func([]string) (string, error) { return strings.ToLower(pick(words)), nil }
	e.funcs["randomDomainSuffix"] = func([]string) (string, error) { return pick(tlds), nil }
	e.funcs["randomProtocol"] = func([]string) (string, error) { return pick([]string{"http", "https"}), nil }
	e.funcs["randomSemver"] = func([]string) (string, error) {
		return fmt.Sprintf("%d.%d.%d", randN(10), randN(20), randN(30)), nil
	}

	ipv4Fn := func([]string) (string, error) {
		return fmt.Sprintf("%d.%d.%d.%d", randRange(1, 254), randN(256), randN(256), randRange(1, 254)), nil
	}
	e.funcs["randomIpv4"] = ipv4Fn // task/AUK spelling
	e.funcs["randomIP"] = ipv4Fn   // Postman: $randomIP

	ipv6Fn := func([]string) (string, error) {
		groups := make([]string, 8)
		for i := range groups {
			groups[i] = fmt.Sprintf("%04x", randN(0x10000))
		}
		return strings.Join(groups, ":"), nil
	}
	e.funcs["randomIpv6"] = ipv6Fn // task/AUK spelling
	e.funcs["randomIPV6"] = ipv6Fn // Postman: $randomIPV6

	macFn := func([]string) (string, error) {
		parts := make([]string, 6)
		for i := range parts {
			parts[i] = fmt.Sprintf("%02x", randN(256))
		}
		return strings.Join(parts, ":"), nil
	}
	e.funcs["randomMacAddress"] = macFn // task/AUK spelling
	e.funcs["randomMACAddress"] = macFn // Postman: $randomMACAddress

	e.funcs["randomPassword"] = func([]string) (string, error) {
		n := randRange(10, 16)
		b := make([]byte, n)
		for i := range b {
			b[i] = passwordChars[randN(len(passwordChars))]
		}
		return string(b), nil
	}
	e.funcs["randomBase64"] = func([]string) (string, error) {
		b := make([]byte, randRange(12, 24))
		if _, err := crand.Read(b); err != nil {
			for i := range b {
				b[i] = byte(randN(256))
			}
		}
		return base64.StdEncoding.EncodeToString(b), nil
	}

	// --- Phone, location, business ----------------------------------------
	e.funcs["randomPhoneNumber"] = func([]string) (string, error) {
		return fmt.Sprintf("(%d%s) %s-%s", randRange(2, 9), randDigits(2), randDigits(3), randDigits(4)), nil
	}
	e.funcs["randomCity"] = func([]string) (string, error) { return pick(cities), nil }
	e.funcs["randomStreetName"] = func([]string) (string, error) {
		return pick(streets) + " " + pick(streetTypes), nil
	}
	e.funcs["randomStreetAddress"] = func([]string) (string, error) {
		return strconv.Itoa(randRange(1, 9999)) + " " + pick(streets) + " " + pick(streetTypes), nil
	}
	e.funcs["randomCountry"] = func([]string) (string, error) { return pick(countries), nil }
	e.funcs["randomCountryCode"] = func([]string) (string, error) { return pick(countryCodes), nil }
	e.funcs["randomCurrencyCode"] = func([]string) (string, error) { return pick(currencyCodes), nil }
	e.funcs["randomCompanyName"] = func([]string) (string, error) {
		return pick(lastNames) + " " + pick(companySuffixes), nil
	}

	// --- Dates (ISO 8601 / RFC3339) ---------------------------------------
	e.funcs["randomDateRecent"] = func([]string) (string, error) {
		return time.Now().Add(-time.Duration(randRange(0, 7*24*3600)) * time.Second).UTC().Format(time.RFC3339), nil
	}
	e.funcs["randomDatePast"] = func([]string) (string, error) {
		return time.Now().Add(-time.Duration(randRange(24*3600, 365*24*3600)) * time.Second).UTC().Format(time.RFC3339), nil
	}
	e.funcs["randomDateFuture"] = func([]string) (string, error) {
		return time.Now().Add(time.Duration(randRange(24*3600, 365*24*3600)) * time.Second).UTC().Format(time.RFC3339), nil
	}
}

// ---------------------------------------------------------------------------
// Randomness helpers. crypto/rand is the primary source (fresh every call, no
// seed, no time dependency); math/rand/v2 (itself per-process auto-seeded) is
// a fallback for the vanishingly rare case crypto/rand's reader errors.
// ---------------------------------------------------------------------------

// randN returns a uniform integer in [0, n). It returns 0 for n <= 0.
func randN(n int) int {
	if n <= 0 {
		return 0
	}
	bn, err := crand.Int(crand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return mrand.IntN(n)
	}
	return int(bn.Int64())
}

// randRange returns a uniform integer in [lo, hi] inclusive (bounds are
// swapped if given reversed).
func randRange(lo, hi int) int {
	if hi < lo {
		lo, hi = hi, lo
	}
	return lo + randN(hi-lo+1)
}

// pick returns a uniformly random element of s ("" if empty).
func pick(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[randN(len(s))]
}

// randHex returns 2*nBytes lowercase hex characters.
func randHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := crand.Read(b); err != nil {
		for i := range b {
			b[i] = byte(randN(256))
		}
	}
	return hex.EncodeToString(b)
}

const alnumChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// randAlnum returns n random alphanumeric characters ("" for n <= 0).
func randAlnum(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = alnumChars[randN(len(alnumChars))]
	}
	return string(b)
}

// randDigits returns n random decimal digits.
func randDigits(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('0' + randN(10))
	}
	return string(b)
}

const passwordChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#$%^&*-_"

// loremWordsN joins n random lorem words with spaces.
func loremWordsN(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = pick(loremWords)
	}
	return strings.Join(parts, " ")
}

// loremSentence builds a capitalised lorem sentence ending in a period.
func loremSentence() string {
	s := loremWordsN(randRange(6, 12))
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:] + "."
}

// ---------------------------------------------------------------------------
// Hand-rolled word/name lists (no external faker dependency). Kept small,
// neutral, and tasteful — a few dozen entries each is plenty for plausible
// sample values.
// ---------------------------------------------------------------------------

var firstNames = []string{
	"James", "Mary", "John", "Patricia", "Robert", "Jennifer", "Michael", "Linda",
	"William", "Elizabeth", "David", "Barbara", "Richard", "Susan", "Joseph", "Jessica",
	"Thomas", "Sarah", "Charles", "Karen", "Daniel", "Nancy", "Matthew", "Lisa",
	"Anthony", "Margaret", "Mark", "Sandra", "Steven", "Emily", "Andrew", "Olivia",
	"Amina", "Kenji", "Sofia", "Mateo", "Priya", "Omar", "Lena", "Diego",
}

var lastNames = []string{
	"Smith", "Johnson", "Williams", "Brown", "Jones", "Garcia", "Miller", "Davis",
	"Rodriguez", "Martinez", "Hernandez", "Lopez", "Gonzalez", "Wilson", "Anderson",
	"Taylor", "Moore", "Jackson", "Martin", "Lee", "Perez", "Thompson", "White",
	"Harris", "Sanchez", "Clark", "Ramirez", "Lewis", "Robinson", "Walker", "Young",
	"Allen", "King", "Wright", "Scott", "Torres", "Nguyen", "Hill", "Flores", "Kim",
}

var namePrefixes = []string{"Mr", "Mrs", "Ms", "Miss", "Dr"}
var nameSuffixes = []string{"Jr", "Sr", "II", "III", "IV", "PhD", "MD", "DDS"}

var words = []string{
	"Table", "Bicycle", "Keyboard", "Mountain", "River", "Engine", "Garden", "Bottle",
	"Camera", "Planet", "Rocket", "Bridge", "Forest", "Island", "Lantern", "Meadow",
	"Pebble", "Quartz", "Signal", "Tunnel", "Valley", "Willow", "Anchor", "Basket",
	"Cabin", "Diamond", "Ember", "Falcon", "Glacier", "Harbor", "Jungle", "Kettle",
	"Ladder", "Marble", "Nectar", "Orchid", "Pyramid", "Ribbon", "Saddle", "Thunder",
}

var loremWords = []string{
	"lorem", "ipsum", "dolor", "sit", "amet", "consectetur", "adipiscing", "elit",
	"sed", "do", "eiusmod", "tempor", "incididunt", "ut", "labore", "et", "dolore",
	"magna", "aliqua", "enim", "ad", "minim", "veniam", "quis", "nostrud",
	"exercitation", "ullamco", "laboris", "nisi", "aliquip", "ex", "ea", "commodo",
	"consequat", "duis", "aute", "irure", "in", "reprehenderit", "voluptate", "velit",
	"esse", "cillum", "fugiat", "nulla", "pariatur", "excepteur", "sint", "occaecat",
	"cupidatat", "non", "proident", "sunt", "culpa", "qui", "officia", "deserunt",
	"mollit", "anim", "id", "est", "laborum",
}

var colorNames = []string{
	"red", "green", "blue", "cyan", "magenta", "yellow", "black", "white", "gray",
	"maroon", "olive", "lime", "teal", "navy", "purple", "silver", "gold", "coral",
	"salmon", "indigo", "violet", "turquoise", "tan", "orchid", "fuchsia", "azure",
	"lavender", "crimson", "plum", "sienna",
}

var jobDescriptors = []string{
	"Lead", "Senior", "Direct", "Corporate", "Dynamic", "Future", "Product", "National",
	"Regional", "District", "Central", "Global", "Customer", "Investor", "Principal",
	"Chief", "Internal", "Forward", "Human", "International",
}

var jobAreas = []string{
	"Solutions", "Program", "Brand", "Security", "Research", "Marketing", "Directives",
	"Implementation", "Integration", "Functionality", "Response", "Paradigm", "Tactics",
	"Identity", "Markets", "Group", "Division", "Applications", "Optimization",
	"Operations", "Infrastructure", "Communications", "Quality", "Data", "Creative",
}

var jobTypes = []string{
	"Supervisor", "Associate", "Executive", "Liaison", "Officer", "Manager", "Engineer",
	"Specialist", "Director", "Coordinator", "Administrator", "Architect", "Analyst",
	"Designer", "Planner", "Technician", "Developer", "Producer", "Consultant", "Strategist",
}

var companySuffixes = []string{
	"Inc", "LLC", "Group", "Ltd", "Co", "and Sons", "and Partners", "Holdings",
	"Industries", "Systems", "Solutions", "Technologies", "Enterprises", "Corp",
}

var emailDomains = []string{
	"example.com", "example.org", "example.net", "gmail.com", "yahoo.com",
	"outlook.com", "proton.me", "company.io", "mail.dev", "acme.co",
}

var tlds = []string{
	"com", "net", "org", "io", "dev", "co", "app", "info", "biz", "me", "xyz", "tech",
}

var cities = []string{
	"Springfield", "Riverside", "Franklin", "Greenville", "Bristol", "Clinton",
	"Fairview", "Salem", "Georgetown", "Madison", "Arlington", "Ashland", "Burlington",
	"Manchester", "Oxford", "Kingston", "Milton", "Auburn", "Dover", "Newport",
}

var streets = []string{
	"Main", "Oak", "Maple", "Cedar", "Pine", "Elm", "Washington", "Lake", "Hill",
	"Park", "Sunset", "Lincoln", "Church", "River", "Spring", "Highland", "Union",
	"Center", "Ridge", "Willow",
}

var streetTypes = []string{"St", "Ave", "Blvd", "Rd", "Ln", "Dr", "Ct", "Way", "Pl"}

var countries = []string{
	"United States", "United Kingdom", "Germany", "France", "Japan", "Canada",
	"Australia", "Brazil", "India", "Spain", "Italy", "Netherlands", "Sweden",
	"Norway", "Mexico", "South Korea", "Singapore", "Ireland", "New Zealand", "Portugal",
}

var countryCodes = []string{
	"US", "GB", "DE", "FR", "JP", "CN", "IN", "BR", "CA", "AU", "MX", "IT", "ES", "NL",
	"SE", "NO", "FI", "DK", "PL", "RU", "KR", "SG", "CH", "IE", "NZ", "ZA", "AR", "PT",
	"GR", "TR", "AE", "SA", "TH", "VN", "ID", "MY", "PH",
}

var currencyCodes = []string{
	"USD", "EUR", "GBP", "JPY", "AUD", "CAD", "CHF", "CNY", "INR", "BRL", "SEK", "NOK",
	"MXN", "SGD", "KRW", "ZAR", "NZD", "HKD",
}
