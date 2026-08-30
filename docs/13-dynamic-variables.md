# Dynamic variables (faker-style template functions)

AUK's template grammar is `${...}`. On top of the core functions (`${uuid}`,
`${timestamp.*}`, `${hash.*(...)}`, `${encode.*(...)}`, `${cookie(...)}`,
`${response('Name').path}`) it ships a **palette of dynamic variables** — the
same faker-backed random values Postman and Insomnia expose as
`{{$random...}}`.

Every name below matches Postman's dynamic-variable name **minus the `$`
prefix**, so an imported Postman collection resolves after a purely mechanical
`{{$name}}` → `${name}` rewrite — including the two non-`random` ones,
`$timestamp` and `$isoTimestamp`. Postman's dynamic variables never
take arguments, so each function works **bare** (`${randomInt}`); AUK adds
optional arguments to a handful using the normal call syntax
(`${randomInt(1,100)}`).

## How they resolve

- **Bare form** — `${randomEmail}` calls the function with no arguments. This
  is the form imported Postman collections use.
- **Call form** — `${randomInt(1, 100)}` passes arguments, parsed exactly like
  every other template function (comma-separated, whitespace-trimmed,
  surrounding quotes stripped).
- **Freshness** — randomness is drawn fresh on every evaluation
  (crypto/rand-backed, with a per-process math/rand/v2 fallback), so two
  `${randomInt}` in one request give two different values. There is no seed or
  per-run cache to configure.
- **Precedence** — an environment variable named the same as a dynamic
  variable still wins (variables are resolved before the function registry).

## Full list

Example outputs are illustrative — each call is random.

### Identifiers

| Function | Example | Postman |
| --- | --- | --- |
| `${randomUuid}` | `ffd72a7b-a864-4b6a-8312-2a25f3303318` | `$randomUUID` (also `randomUUID`, `guid`) |
| `${guid}` | `cad48fa1-e40d-4985-9b73-1fc647a15ca9` | `$guid` |

### Timestamps

| Function | Example | Postman |
| --- | --- | --- |
| `${timestamp}` | `1787059200` | `$timestamp` — unix **seconds** |
| `${isoTimestamp}` | `2026-08-30T12:34:56.789Z` | `$isoTimestamp` — UTC, millisecond precision + `Z`, matching JavaScript's `Date.toISOString()` |

These two are the bare Postman spellings, registered alongside the faker
palette in `internal/templating/random.go` precisely so an imported collection
resolves. AUK's own dotted forms (`${timestamp.unix}`, `${timestamp.unixMillis}`,
`${timestamp.iso8601}`, plus `${timestamp.offset(...)}` and
`${timestamp.format(...)}`) are unchanged and live in the core builtins;
`${timestamp.iso8601}` differs from `${isoTimestamp}` only in dropping the
milliseconds.

### Numbers, text, booleans

| Function | Example | Notes |
| --- | --- | --- |
| `${randomInt}` / `${randomInt(min,max)}` | `847` / `79` | default range `0..1000`; args inclusive |
| `${randomAlphaNumeric}` / `${randomAlphaNumeric(len)}` | `k` / `2TJiC79S4OvY` | bare = 1 char (Postman parity); `(len)` = that many |
| `${randomBoolean}` | `false` | `true` / `false` |
| `${randomColor}` | `violet` | human-readable color name |
| `${randomHexColor}` | `#9d21c4` | `^#[0-9a-f]{6}$` (AUK extension) |
| `${randomPrice}` / `${randomPrice(min,max)}` | `494.55` / `43.09` | 2 decimals; default `0..1000` |

### Words & lorem

| Function | Example |
| --- | --- |
| `${randomWord}` | `Ember` |
| `${randomWords}` | `Cabin Marble Signal` |
| `${randomLoremWord}` | `aute` |
| `${randomLoremWords}` | `do adipiscing nisi nostrud id` |
| `${randomLoremSentence}` | `Officia est exercitation irure consectetur incididunt cupidatat sint culpa.` |
| `${randomLoremParagraph}` | `Dolore sint voluptate magna velit incididunt. Elit sed deserunt do sed reprehenderit ex ipsum aliqua.` |

### People

| Function | Example |
| --- | --- |
| `${randomFirstName}` | `Diego` |
| `${randomLastName}` | `Jackson` |
| `${randomFullName}` | `Elizabeth Wilson` |
| `${randomNamePrefix}` | `Ms` |
| `${randomNameSuffix}` | `MD` |
| `${randomJobTitle}` | `District Solutions Manager` |
| `${randomCompanyName}` | `Thompson Solutions` |

### Internet, domains, emails

| Function | Example | Notes |
| --- | --- | --- |
| `${randomEmail}` | `sofia.williams@yahoo.com` | contains `@` and a dotted domain |
| `${randomExampleEmail}` | `olivia.clark87@mail.dev` | Postman `$randomExampleEmail` |
| `${randomUserName}` | `Mark75` | |
| `${randomUrl}` | `https://pebble.biz` | |
| `${randomDomainName}` | `orchid.org` | |
| `${randomDomainWord}` | `tunnel` | |
| `${randomDomainSuffix}` | `co` | |
| `${randomProtocol}` | `https` | `http` / `https` |
| `${randomSemver}` | `4.0.27` | |
| `${randomIpv4}` / `${randomIP}` | `85.197.27.135` | four `0-255` octets |
| `${randomIpv6}` / `${randomIPV6}` | `4e77:3946:3b38:6fbd:44b4:6d17:6f16:4f05` | eight hextets |
| `${randomMacAddress}` / `${randomMACAddress}` | `fe:a8:a5:9f:59:fe` | |
| `${randomPassword}` | `!2Ut3^Y5!IbSx` | 10–16 mixed chars |
| `${randomBase64}` | `pcgSKemmuxifLtj+` | std base64 of random bytes |

### Phone & location

| Function | Example |
| --- | --- |
| `${randomPhoneNumber}` | `(251) 111-7657` |
| `${randomCity}` | `Manchester` |
| `${randomStreetName}` | `Main Way` |
| `${randomStreetAddress}` | `5846 Center Ave` |
| `${randomCountry}` | `Netherlands` |
| `${randomCountryCode}` | `ID` (ISO 3166-1 alpha-2) |
| `${randomCurrencyCode}` | `JPY` (ISO 4217) |

### Dates (ISO 8601 / RFC 3339)

| Function | Example | Range |
| --- | --- | --- |
| `${randomDateRecent}` | `2026-08-24T14:50:45Z` | within the last 7 days |
| `${randomDatePast}` | `2026-08-03T07:53:31Z` | 1 day – 1 year ago |
| `${randomDateFuture}` | `2027-03-28T01:21:23Z` | 1 day – 1 year ahead |

> Postman emits its date variables as a JS `Date.toString()` (`"Tue Aug 30
> 2026 …"`). AUK emits ISO 8601 / RFC 3339 instead — a valid, unambiguous date
> string that parses everywhere. This is the one place a value differs in
> *shape* from Postman rather than 1:1.

## Postman name mapping

**1:1 (name identical minus `$`):** `timestamp`, `isoTimestamp`, `randomUUID`, `guid`, `randomInt`,
`randomAlphaNumeric`, `randomBoolean`, `randomColor`, `randomPrice`,
`randomWord`, `randomWords`, `randomLoremWord`, `randomLoremWords`,
`randomLoremSentence`, `randomLoremParagraph`, `randomFirstName`,
`randomLastName`, `randomFullName`, `randomNamePrefix`, `randomNameSuffix`,
`randomJobTitle`, `randomCompanyName`, `randomEmail`, `randomExampleEmail`,
`randomUserName`, `randomUrl`, `randomDomainName`, `randomDomainWord`,
`randomDomainSuffix`, `randomProtocol`, `randomSemver`, `randomIP`,
`randomIPV6`, `randomMACAddress`, `randomPassword`, `randomPhoneNumber`,
`randomCity`, `randomStreetName`, `randomStreetAddress`, `randomCountry`,
`randomCountryCode`, `randomCurrencyCode`, `randomDateRecent`,
`randomDatePast`, `randomDateFuture`.

**AUK spelling aliases** (registered in addition to the Postman name, so both
resolve): `randomUuid` (↔ `randomUUID`), `randomIpv4` (↔ `randomIP`),
`randomIpv6` (↔ `randomIPV6`), `randomMacAddress` (↔ `randomMACAddress`).

**AUK extensions** (no exact Postman equivalent): `randomHexColor`, and the
optional-argument forms `randomInt(min,max)`, `randomAlphaNumeric(len)`,
`randomPrice(min,max)`.

Postman dynamic variables not yet mapped (e.g. `$randomBankAccount`,
`$randomProductName`, `$randomFileName`, image URLs) fall through to the normal
"unresolved variable" error; they can be added to
`internal/templating/random.go` following the same pattern.

> The importer only rewrites `{{$name}}` and names the collection actually
> defines — an unknown `{{name}}` is left literal so a Handlebars/Mustache
> payload still sends. See the conversion rules in
> `internal/importer/postman.go`.
