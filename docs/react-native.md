# BS/AD date picker in React Native

This guide covers everything a React Native (Expo or bare) app needs to show a **Bikram Sambat (BS) /
Gregorian (AD) date picker** and calendar driven by this API. The app works offline, and admins change
its look and data without a new store build.

- [1. Is it available on mobile?](#1-is-it-available-on-mobile)
- [2. How it works](#2-how-it-works)
- [3. What changes without a new build](#3-what-changes-without-a-new-build)
- [4. Setup](#4-setup)
- [5. Implementation](#5-implementation)
- [6. Using the picker in screens](#6-using-the-picker-in-screens)
- [7. How an admin change reaches phones](#7-how-an-admin-change-reaches-phones)
- [8. Shipping code changes without store review (EAS Update)](#8-shipping-code-changes-without-store-review-eas-update)
- [9. Testing checklist](#9-testing-checklist)
- [10. Common problems](#10-common-problems)

---

## 1. Is it available on mobile?

Yes. The API serves the same data and theme to websites and mobile apps. For React Native you add a few
files to your app (all in section 5). There is no npm package and no native module, so it runs in Expo
Go, in development builds and in bare React Native.

What you get:

| Feature | Details |
|---------|---------|
| Date picker (bottom sheet) | BS or AD month grid, switch between calendars, both dates in every cell, today, weekends, holidays and event dots |
| Date field | A tappable field that shows the picked date in BS and AD and opens the picker |
| Nepali or English | Month and weekday names, Devanagari or Latin digits |
| Light and dark mode | Follows the phone, or forced by the admin |
| Holidays and events | Published by admins, with category colours |
| Offline | Works with no network at all, from the first launch |
| Remote control | Colours, defaults, events and year-table corrections change without an app release |

## 2. How it works

```
 Admin dashboard ──► API ──► manifest (versions of everything)
                          ├─► year table       /v1/calendar/data/{v}        ~10 KB, immutable
                          ├─► UI config        /v1/ui-config/mobile/{v}     theme + defaults, immutable
                          └─► event buckets    /v1/events/buckets/{y}/{v}   one per BS year, immutable

 Phone:  bundled snapshot ─► device cache ─► picker renders instantly (no network needed)
                                  ▲
                 sync engine ─────┘  on start, on return to foreground, every 15 minutes
```

1. **The app converts dates itself.** It stores the BS year table (the start date and month lengths of
   every BS year, 1975–2100) and converts with plain arithmetic. The picker never waits for the network.
2. **The manifest says what changed.** `GET /v1/manifest?app=mobile` is small and returns version
   numbers for the year table, the UI config and each year's events. The app compares them with its
   cache and downloads only what changed. When nothing changed the server answers `304` with no body.
3. **Everything else is immutable.** Each version has its own URL and never changes, so the app can
   keep it forever.
4. **The app ships with a snapshot.** A copy of the year table and the default theme is bundled, so the
   picker works on first launch in airplane mode.
5. **Bad data is rejected.** A downloaded year table is checked (SHA-256 checksum and the table rules)
   before it replaces the old one. A bad theme field falls back to its default, and the rest still applies.

## 3. What changes without a new build

Admins change these in the dashboard. Phones pick them up at the next sync (at the latest 15 minutes
while the app is open, or when the app comes back to the foreground). **No store review is involved**,
because the app only downloads data, not code.

| Change in the dashboard | What users see |
|-------------------------|----------------|
| Theme colours (light and dark): background, text, primary, today, holiday, weekend… | New colours in the picker |
| Colour scheme: system, always light or always dark | Picker follows it |
| Default calendar: BS or AD | Picker opens in that calendar |
| Language (`en`/`ne`) and digits (Latin/Devanagari) | Names and numbers switch |
| Week start and weekend days | Grid layout and red weekend days |
| Show the other calendar's date, event dots, max dots per day, holiday highlight, the BS/AD switch | Picker elements appear or hide |
| Corner radius | Rounder or squarer picker |
| Publish, edit or remove a holiday or event | Appears or disappears on its dates |
| Category colour changes | Event dots recolour |
| Year-table correction (approved by a second admin) | Month lengths and conversions update |
| Staged rollout (for example 20 % of phones) and one-click rollback | Only some phones get the new theme; rollback returns everyone to the old one |
| `minSupportedClient` raised | Old app versions show an "update the app" message |

**What does need a new build:** changing the app's code (a new screen, a different picker layout, a new
library). For JavaScript-only code there is a second route that also skips store review: EAS Update
(section 8). Native changes (new native modules, permissions, the app icon) always need a store build.

## 4. Setup

### 4.1 Packages

```bash
npx create-expo-app@latest my-app && cd my-app          # or use your existing app
npx expo install @react-native-async-storage/async-storage expo-crypto expo-constants
```

- `async-storage`: the device cache.
- `expo-crypto`: SHA-256 to verify the year table, and a random install id for staged rollouts.
- `expo-constants`: the app version, sent to the manifest so the server can require an update.

Bare React Native: install `expo` modules with `npx install-expo-modules@latest` first, or replace
`expo-crypto` with any SHA-256 and UUID library.

### 4.2 API address and key

`.env` in the app (Expo reads `EXPO_PUBLIC_*` variables at build time):

```bash
EXPO_PUBLIC_CAL_API=http://192.168.1.20:8080       # see the table below
EXPO_PUBLIC_CAL_KEY=pk_your_mobile_key
EXPO_PUBLIC_CAL_APP=mobile                          # the UI-config app key admins edit
```

| App runs on | Use |
|-------------|-----|
| Android emulator | `http://10.0.2.2:8080` (the emulator's name for your computer) |
| iOS simulator | `http://localhost:8080` |
| A real phone on the same Wi-Fi | `http://<your computer's LAN IP>:8080` |
| Production | `https://calendar-api.example.org` (release builds block plain `http`) |

Ask an admin for a **public** key **without** `allowedOrigins`. Mobile apps send no `Origin` header, so
CORS does not apply. In development use `pk_dev_local_public_key_0001`.

```bash
curl -X POST https://calendar-api.example.org/v1/admin/api-clients -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"Mobile app","kind":"public"}'
```

In `app.json`, set `"userInterfaceStyle": "automatic"`. Without it the phone always reports light mode.

### 4.3 Bundle a snapshot

Download the current year table and the default theme into the app, so the first launch works offline.
Run this before each store build (for example in CI). It is fine if the snapshot is old: the app
updates it at the first sync.

`scripts/fetch-calendar-snapshot.mjs`:

```js
// node scripts/fetch-calendar-snapshot.mjs   (Node 18+)
import { mkdir, writeFile } from "node:fs/promises";

const API = process.env.EXPO_PUBLIC_CAL_API;
const KEY = process.env.EXPO_PUBLIC_CAL_KEY;
const get = async (path) => {
  const res = await fetch(API + path, { headers: { "X-Api-Key": KEY } });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json();
};

await mkdir("assets/calendar", { recursive: true });
await writeFile("assets/calendar/data.json", JSON.stringify(await get("/v1/calendar/data/latest")));
await writeFile("assets/calendar/config.json", JSON.stringify(await get("/v1/ui-config/mobile/0")));
console.log("calendar snapshot saved");
```

```bash
node --env-file=.env scripts/fetch-calendar-snapshot.mjs
```

### 4.4 Files you will add

```
lib/calendar/
  types.ts          API types
  engine.ts         BS ⇄ AD conversion and month grids, on the device
  config.ts         UI config with per-field fallback
  storage.ts        device cache
  sync.ts           manifest sync (versions, checksum, rollout, events)
  CalendarProvider.tsx   React context: cache + sync + theme
components/
  DatePicker.tsx    the bottom-sheet picker
  DateField.tsx     a field that opens the picker
```

## 5. Implementation

### 5.1 Types

`lib/calendar/types.ts`:

```ts
export type Basis = "AD" | "BS";
export type YearStatus = "verified" | "projected";
export type Localized = { en: string; ne?: string };

export interface YearRow { y: number; start: string; days: number[]; status: YearStatus }

export interface CalendarData {
  version: number;
  sha256: string;
  minYear: number;
  maxYear: number;
  years: YearRow[];
}

export interface Occurrence {
  id: string;
  eventId: string;
  title: Localized;
  description: Localized | null;
  category: string;
  isHoliday: boolean;
  start: { ad: string; bs: string | null };
  end: { ad: string; bs: string | null };
  allDay: boolean;
  startTime: string | null;
  endTime: string | null;
  color: { light: string; dark: string };
}

export interface EventBucket {
  bsYear: number;
  version: number;
  status: YearStatus;
  range: { start: string; end: string };
  events: Occurrence[];
}

export interface Manifest {
  dataVersion: number;
  dataSha256: string;
  supportedRange: { minBsYear: number; maxBsYear: number; minAd: string; maxAd: string };
  currentBsYear: number;
  firstProjectedBsYear: number | null;
  config: { app: string; version: number; minClientVersion: string; candidate: { version: number; percent: number } | null };
  eventBuckets: Record<string, number>;
  defaultBucketVersion: number;
  minSupportedClient: string;
  updateRequired: boolean;
  links: { data: string; config: string; bucketTemplate: string };
  serverTime: string;
}

export type TokenName =
  | "bg" | "surface" | "text" | "muted" | "border" | "primary" | "onPrimary"
  | "today" | "holiday" | "weekend" | "disabled";
export type ThemeTokens = Record<TokenName, string>;

export interface UiConfig {
  schemaVersion: number;
  defaults: {
    mode: Basis;
    locale: "en" | "ne";
    digits: "latin" | "devanagari";
    weekStart: number;                     // 0 = Sunday
    weekendDays: number[];
    todayTimeZone: "device" | "Asia/Kathmandu";
    colorScheme: "system" | "light" | "dark";
  };
  display: {
    allowModeSwitch: boolean;
    showSecondaryDate: boolean;
    showEventDots: boolean;
    maxDotsPerDay: number;
    highlightHolidays: boolean;
    showProjectedWarning: boolean;
  };
  theme: { light: ThemeTokens; dark: ThemeTokens };
  shape: { radius: number };
}
```

### 5.2 Conversion engine

The engine turns every date into an **epoch day** (days since 1970-01-01), so time zones can never shift
a date. AD dates use calendar arithmetic; BS dates use the year table. It never uses JavaScript `Date`
for calendar dates.

`lib/calendar/engine.ts`:

```ts
import type { Basis, CalendarData, YearStatus } from "./types";

export type YMD = { year: number; month: number; day: number };

export const pad = (n: number) => String(n).padStart(2, "0");
export const fmt = (d: YMD) => `${d.year}-${pad(d.month)}-${pad(d.day)}`;
export function parse(s: string): YMD {
  const [year, month, day] = s.split("-").map(Number);
  return { year: year!, month: month!, day: day! };
}

// ---- AD (proleptic Gregorian) ⇄ epoch day -----------------------------------------------

export function daysFromCivil(y: number, m: number, d: number): number {
  y -= m <= 2 ? 1 : 0;
  const era = Math.floor(y / 400);
  const yoe = y - era * 400;
  const doy = Math.floor((153 * ((m + 9) % 12) + 2) / 5) + d - 1;
  const doe = yoe * 365 + Math.floor(yoe / 4) - Math.floor(yoe / 100) + doy;
  return era * 146097 + doe - 719468;
}

export function civilFromDays(n: number): YMD {
  const z = n + 719468;
  const era = Math.floor(z / 146097);
  const doe = z - era * 146097;
  const yoe = Math.floor((doe - Math.floor(doe / 1460) + Math.floor(doe / 36524) - Math.floor(doe / 146096)) / 365);
  const doy = doe - (365 * yoe + Math.floor(yoe / 4) - Math.floor(yoe / 100));
  const mp = Math.floor((5 * doy + 2) / 153);
  const day = doy - Math.floor((153 * mp + 2) / 5) + 1;
  const month = mp < 10 ? mp + 3 : mp - 9;
  return { year: yoe + era * 400 + (month <= 2 ? 1 : 0), month, day };
}

/** 0 = Sunday. 1970-01-01 was a Thursday. */
export const weekday = (n: number) => (((n + 4) % 7) + 7) % 7;

/** Today's epoch day in Nepal (UTC+5:45, no daylight saving) or on the device's clock. */
export function todayEpoch(zone: "device" | "Asia/Kathmandu", clockOffsetMs = 0): number {
  const now = Date.now() + clockOffsetMs;
  if (zone === "Asia/Kathmandu") return Math.floor((now + 345 * 60_000) / 86_400_000);
  const d = new Date(now);
  return daysFromCivil(d.getFullYear(), d.getMonth() + 1, d.getDate());
}

// ---- table checks (same rules as the server, docs/reliable.md §3.2) -----------------------

export function checkTable(data: CalendarData): string[] {
  const errors: string[] = [];
  const ys = data.years;
  if (!ys.length) return ["empty table"];
  ys.forEach((r, i) => {
    if (i > 0 && r.y !== ys[i - 1]!.y + 1) errors.push(`year ${r.y} is not contiguous`);
    if (r.days.length !== 12) errors.push(`year ${r.y} does not have 12 months`);
    if (r.days.some((d) => d < 29 || d > 32)) errors.push(`year ${r.y} has a month outside 29-32 days`);
    const len = r.days.reduce((a, b) => a + b, 0);
    if (len < 364 || len > 367) errors.push(`year ${r.y} has ${len} days`);
    if (i > 0) {
      const prev = ys[i - 1]!;
      const p = parse(prev.start);
      const expected = daysFromCivil(p.year, p.month, p.day) + prev.days.reduce((a, b) => a + b, 0);
      const s = parse(r.start);
      if (daysFromCivil(s.year, s.month, s.day) !== expected) errors.push(`year ${r.y} does not start after ${prev.y}`);
    }
  });
  if (data.minYear !== ys[0]!.y || data.maxYear !== ys[ys.length - 1]!.y) errors.push("minYear/maxYear mismatch");
  return errors;
}

/** The canonical text whose SHA-256 is `sha256` in the table and `dataSha256` in the manifest. */
export const canonicalText = (data: CalendarData) =>
  data.years.map((y) => [y.y, y.start, y.days.join(","), y.status].join(":") + "\n").join("");

// ---- the table ------------------------------------------------------------------------------

export interface Cell {
  epoch: number;
  ad: string;
  bs: string | null;      // null outside the table
  day: number;            // day number in the grid's calendar
  weekday: number;
  inMonth: boolean;
}

export class CalendarTable {
  readonly version: number;
  readonly minYear: number;
  readonly maxYear: number;
  readonly minEpoch: number;
  readonly maxEpoch: number;
  private readonly starts: number[];

  constructor(private readonly data: CalendarData) {
    this.version = data.version;
    this.minYear = data.minYear;
    this.maxYear = data.maxYear;
    this.starts = data.years.map((r) => {
      const s = parse(r.start);
      return daysFromCivil(s.year, s.month, s.day);
    });
    const last = data.years[data.years.length - 1]!;
    this.minEpoch = this.starts[0]!;
    this.maxEpoch = this.starts[this.starts.length - 1]! + last.days.reduce((a, b) => a + b, 0) - 1;
  }

  status(bsYear: number): YearStatus | null {
    return this.data.years[bsYear - this.minYear]?.status ?? null;
  }

  inRange(n: number) {
    return n >= this.minEpoch && n <= this.maxEpoch;
  }

  daysInMonth(basis: Basis, year: number, month: number): number | null {
    if (month < 1 || month > 12) return null;
    if (basis === "AD") {
      const next = month === 12 ? daysFromCivil(year + 1, 1, 1) : daysFromCivil(year, month + 1, 1);
      return next - daysFromCivil(year, month, 1);
    }
    return this.data.years[year - this.minYear]?.days[month - 1] ?? null;
  }

  /** Epoch day of a BS date, or null if it does not exist. */
  bsToEpoch({ year, month, day }: YMD): number | null {
    const i = year - this.minYear;
    const row = this.data.years[i];
    if (!row || month < 1 || month > 12 || day < 1 || day > row.days[month - 1]!) return null;
    let n = this.starts[i]!;
    for (let m = 0; m < month - 1; m++) n += row.days[m]!;
    return n + day - 1;
  }

  /** BS date of an epoch day, or null outside the table. */
  epochToBS(n: number): YMD | null {
    if (!this.inRange(n)) return null;
    let lo = 0;
    let hi = this.starts.length - 1;
    while (lo < hi) {                                  // last year starting on or before n
      const mid = (lo + hi + 1) >> 1;
      if (this.starts[mid]! <= n) lo = mid;
      else hi = mid - 1;
    }
    const row = this.data.years[lo]!;
    let off = n - this.starts[lo]!;
    let month = 0;
    while (off >= row.days[month]!) off -= row.days[month++]!;
    return { year: row.y, month: month + 1, day: off + 1 };
  }

  toEpoch(basis: Basis, d: YMD): number | null {
    if (basis === "BS") return this.bsToEpoch(d);
    const dim = this.daysInMonth("AD", d.year, d.month);
    return dim && d.day >= 1 && d.day <= dim ? daysFromCivil(d.year, d.month, d.day) : null;
  }

  /** Both dates of an epoch day. */
  convert(n: number) {
    const bs = this.epochToBS(n);
    return { epoch: n, ad: fmt(civilFromDays(n)), bs: bs ? fmt(bs) : null, weekday: weekday(n) };
  }

  /** 42 cells (6 weeks) for a month, starting on weekStart. Null if the month is outside the table. */
  monthGrid(basis: Basis, year: number, month: number, weekStart = 0): Cell[] | null {
    const first = this.toEpoch(basis, { year, month, day: 1 });
    const dim = this.daysInMonth(basis, year, month);
    if (first === null || dim === null) return null;
    const start = first - ((weekday(first) - weekStart + 7) % 7);
    return Array.from({ length: 42 }, (_, i) => {
      const n = start + i;
      const bs = this.epochToBS(n);
      const ad = civilFromDays(n);
      return {
        epoch: n,
        ad: fmt(ad),
        bs: bs ? fmt(bs) : null,
        day: basis === "BS" ? bs?.day ?? 0 : ad.day,
        weekday: weekday(n),
        inMonth: n >= first && n < first + dim,
      };
    });
  }

  /** Is this BS/AD month inside the table (its first and last day both convert)? */
  hasMonth(basis: Basis, year: number, month: number) {
    const first = this.toEpoch(basis, { year, month, day: 1 });
    const dim = this.daysInMonth(basis, year, month);
    return first !== null && dim !== null && this.inRange(first) && this.inRange(first + dim - 1);
  }
}

// ---- names ----------------------------------------------------------------------------------

export const MONTHS = {
  BS: {
    en: ["Baisakh", "Jestha", "Asar", "Shrawan", "Bhadra", "Ashwin", "Kartik", "Mangsir", "Poush", "Magh", "Falgun", "Chaitra"],
    ne: ["बैशाख", "जेठ", "असार", "साउन", "भदौ", "असोज", "कात्तिक", "मंसिर", "पुस", "माघ", "फागुन", "चैत"],
  },
  AD: {
    en: ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"],
    ne: ["जनवरी", "फेब्रुअरी", "मार्च", "अप्रिल", "मे", "जुन", "जुलाई", "अगस्ट", "सेप्टेम्बर", "अक्टोबर", "नोभेम्बर", "डिसेम्बर"],
  },
} as const;

export const WEEKDAYS = {
  en: ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"],
  ne: ["आइत", "सोम", "मंगल", "बुध", "बिही", "शुक्र", "शनि"],
} as const;

export const toDevanagari = (s: string | number) =>
  String(s).replace(/[0-9]/g, (d) => String.fromCharCode(0x0966 + Number(d)));

export function addMonths(year: number, month: number, delta: number) {
  const i = year * 12 + (month - 1) + delta;
  return { year: Math.floor(i / 12), month: (i % 12) + 1 };
}
```

### 5.3 UI config with per-field fallback

A config from the server is merged field by field onto the bundled default. A wrong or missing field
keeps the default, and the rest of the config still applies (docs/reliable.md §3.5). A newer
`schemaVersion` than the app knows is still read field by field.

`lib/calendar/config.ts`:

```ts
import type { ThemeTokens, UiConfig } from "./types";
import bundled from "../../assets/calendar/config.json";

export const DEFAULT_CONFIG = bundled as UiConfig;

const isColor = (v: unknown): v is string => typeof v === "string" && /^#([0-9a-f]{6}|[0-9a-f]{8})$/i.test(v);
const isInt = (v: unknown, lo: number, hi: number): v is number => Number.isInteger(v) && (v as number) >= lo && (v as number) <= hi;
const oneOf = <T extends string>(v: unknown, xs: readonly T[]): v is T => typeof v === "string" && xs.includes(v as T);
const isBool = (v: unknown): v is boolean => typeof v === "boolean";

/** Returns the parsed config and the names of fields that fell back to the default. */
export function parseConfig(raw: any, base: UiConfig = DEFAULT_CONFIG): { config: UiConfig; fallbacks: string[] } {
  const fallbacks: string[] = [];
  const pick = <T,>(path: string, value: unknown, ok: (v: unknown) => v is T, fallback: T): T => {
    if (ok(value)) return value;
    if (value !== undefined) fallbacks.push(path);
    return fallback;
  };
  const d = raw?.defaults ?? {};
  const s = raw?.display ?? {};
  const b = base;

  const tokens = (scheme: "light" | "dark"): ThemeTokens => {
    const out = { ...b.theme[scheme] };
    for (const k of Object.keys(out) as (keyof ThemeTokens)[]) {
      out[k] = pick(`theme.${scheme}.${k}`, raw?.theme?.[scheme]?.[k], isColor, b.theme[scheme][k]);
    }
    return out;
  };

  const config: UiConfig = {
    schemaVersion: typeof raw?.schemaVersion === "number" ? raw.schemaVersion : b.schemaVersion,
    defaults: {
      mode: pick("defaults.mode", d.mode, (v): v is "AD" | "BS" => oneOf(v, ["AD", "BS"] as const), b.defaults.mode),
      locale: pick("defaults.locale", d.locale, (v): v is "en" | "ne" => oneOf(v, ["en", "ne"] as const), b.defaults.locale),
      digits: pick("defaults.digits", d.digits, (v): v is "latin" | "devanagari" => oneOf(v, ["latin", "devanagari"] as const), b.defaults.digits),
      weekStart: pick("defaults.weekStart", d.weekStart, (v): v is number => isInt(v, 0, 6), b.defaults.weekStart),
      weekendDays: pick("defaults.weekendDays", d.weekendDays,
        (v): v is number[] => Array.isArray(v) && v.every((x) => isInt(x, 0, 6)), b.defaults.weekendDays),
      todayTimeZone: pick("defaults.todayTimeZone", d.todayTimeZone,
        (v): v is "device" | "Asia/Kathmandu" => oneOf(v, ["device", "Asia/Kathmandu"] as const), b.defaults.todayTimeZone),
      colorScheme: pick("defaults.colorScheme", d.colorScheme,
        (v): v is "system" | "light" | "dark" => oneOf(v, ["system", "light", "dark"] as const), b.defaults.colorScheme),
    },
    display: {
      allowModeSwitch: pick("display.allowModeSwitch", s.allowModeSwitch, isBool, b.display.allowModeSwitch),
      showSecondaryDate: pick("display.showSecondaryDate", s.showSecondaryDate, isBool, b.display.showSecondaryDate),
      showEventDots: pick("display.showEventDots", s.showEventDots, isBool, b.display.showEventDots),
      maxDotsPerDay: pick("display.maxDotsPerDay", s.maxDotsPerDay, (v): v is number => isInt(v, 0, 10), b.display.maxDotsPerDay),
      highlightHolidays: pick("display.highlightHolidays", s.highlightHolidays, isBool, b.display.highlightHolidays),
      showProjectedWarning: pick("display.showProjectedWarning", s.showProjectedWarning, isBool, b.display.showProjectedWarning),
    },
    theme: { light: tokens("light"), dark: tokens("dark") },
    shape: { radius: pick("shape.radius", raw?.shape?.radius, (v): v is number => isInt(v, 0, 32), b.shape.radius) },
  };
  return { config, fallbacks };
}
```

For `import bundled from "…json"` in TypeScript, set `"resolveJsonModule": true` in `tsconfig.json`
(Expo's default tsconfig already allows it).

### 5.4 Device cache

`lib/calendar/storage.ts`:

```ts
import AsyncStorage from "@react-native-async-storage/async-storage";
import * as Crypto from "expo-crypto";

const P = "bscal:v1:";

export const cache = {
  async get<T>(key: string): Promise<T | null> {
    try {
      const raw = await AsyncStorage.getItem(P + key);
      return raw ? (JSON.parse(raw) as T) : null;
    } catch {
      return null;                                    // a corrupt entry acts like a missing one
    }
  },
  set(key: string, value: unknown) {
    return AsyncStorage.setItem(P + key, JSON.stringify(value)).catch(() => {});
  },
};

/** A random id per install, kept forever. Used only to place this phone in staged rollouts. */
export async function installId(): Promise<string> {
  const saved = await cache.get<string>("installId");
  if (saved) return saved;
  const id = Crypto.randomUUID();
  await cache.set("installId", id);
  return id;
}
```

### 5.5 Sync engine

This follows the reference flow in [api.md §6.1](api.md#61-client-sync-apps-and-websites):
manifest → year table (verified) → UI config (with staged rollout) → the event buckets the app needs.
Every step is independent: a failure keeps what is cached and never blanks the screen.

`lib/calendar/sync.ts`:

```ts
import Constants from "expo-constants";
import * as Crypto from "expo-crypto";
import { canonicalText, checkTable } from "./engine";
import { parseConfig } from "./config";
import { cache, installId } from "./storage";
import type { CalendarData, EventBucket, Manifest, UiConfig } from "./types";

const API = process.env.EXPO_PUBLIC_CAL_API!;
const KEY = process.env.EXPO_PUBLIC_CAL_KEY!;
export const APP = process.env.EXPO_PUBLIC_CAL_APP ?? "mobile";
export const CLIENT_VERSION = Constants.expoConfig?.version ?? "0.0.0";   // "version" in app.json

export interface SyncResult {
  data?: CalendarData;
  config?: UiConfig;
  configVersion?: number;
  buckets?: Record<number, EventBucket>;
  updateRequired?: boolean;
  clockOffsetMs?: number;
}

async function get(path: string, init: RequestInit = {}) {
  const res = await fetch(API + path, { ...init, headers: { "X-Api-Key": KEY, ...(init.headers ?? {}) } });
  if (res.status !== 304 && !res.ok) throw new Error(`${path}: HTTP ${res.status}`);
  return res;
}

// FNV-1a over the UTF-8 bytes (TextEncoder: React Native 0.74+ / Expo SDK 51+).
function fnv1a32(s: string): number {
  let h = 0x811c9dc5;
  for (const b of new TextEncoder().encode(s)) {
    h ^= b;
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h;
}

export function telemetry(type: string, detail = "", dataVersion = 0, configVersion = 0) {
  fetch(`${API}/v1/telemetry`, {
    method: "POST",
    headers: { "X-Api-Key": KEY, "Content-Type": "application/json" },
    body: JSON.stringify({ events: [{ type, app: APP, clientVersion: CLIENT_VERSION, dataVersion, configVersion, detail: detail.slice(0, 500) }] }),
  }).catch(() => {});                               // best effort
}

/**
 * One sync pass. `have` is what the app currently uses; `neededYears` are the BS years whose events it
 * should keep (current year ±1 plus any year the user browsed). Returns only what changed.
 */
export async function syncOnce(have: {
  dataVersion: number;
  configVersion: number;
  bucketVersions: Record<number, number>;
  neededYears: number[];
}): Promise<SyncResult> {
  const out: SyncResult = {};
  const etag = await cache.get<string>("manifestEtag");
  const res = await get(`/v1/manifest?app=${APP}&clientVersion=${CLIENT_VERSION}`,
    { headers: etag ? { "If-None-Match": etag } : {} });

  let m: Manifest | null;
  if (res.status === 304) {
    m = await cache.get<Manifest>("manifest");       // nothing changed since the last pass
  } else {
    m = (await res.json()) as Manifest;
    await cache.set("manifest", m);
    const newTag = res.headers.get("ETag");
    if (newTag) await cache.set("manifestEtag", newTag);
    out.clockOffsetMs = clockOffset(m.serverTime);
  }
  if (!m) return out;
  out.updateRequired = m.updateRequired;

  // 1. Year table: download, verify, then swap. A bad table is reported and ignored.
  if (m.dataVersion !== have.dataVersion) {
    try {
      const data = (await (await get(m.links.data)).json()) as CalendarData;
      const hash = await Crypto.digestStringAsync(Crypto.CryptoDigestAlgorithm.SHA256, canonicalText(data));
      const problems = checkTable(data);
      if (hash !== data.sha256 || hash !== m.dataSha256) problems.unshift("checksum mismatch");
      if (problems.length) {
        telemetry("table_rejected", `v${data.version}: ${problems.join("; ")}`, have.dataVersion, have.configVersion);
      } else {
        await cache.set("data", data);
        out.data = data;
      }
    } catch (e) {
      telemetry("sync_failed", `data: ${String(e)}`, have.dataVersion, have.configVersion);
    }
  }

  // 2. UI config: the candidate applies only to phones inside the rollout percent.
  const c = m.config;
  const wanted = c.candidate && fnv1a32(await installId()) % 100 < c.candidate.percent ? c.candidate.version : c.version;
  if (wanted !== have.configVersion) {
    try {
      const raw = await (await get(`/v1/ui-config/${APP}/${wanted}`)).json();
      const { config, fallbacks } = parseConfig(raw);
      if (fallbacks.length) telemetry("config_field_fallback", fallbacks.join(","), have.dataVersion, wanted);
      await cache.set("config", { version: wanted, config });
      out.config = config;
      out.configVersion = wanted;
      telemetry("config_applied", "", have.dataVersion, wanted);
    } catch (e) {
      telemetry("sync_failed", `config: ${String(e)}`, have.dataVersion, have.configVersion);
    }
  }

  // 3. Events: one immutable bucket per BS year; fetch only those whose version changed.
  const { minBsYear, maxBsYear } = m.supportedRange;
  for (const year of have.neededYears.filter((y) => y >= minBsYear && y <= maxBsYear)) {
    const v = m.eventBuckets[String(year)] ?? m.defaultBucketVersion;
    if (have.bucketVersions[year] === v) continue;
    try {
      const bucket = (await (await get(`/v1/events/buckets/${year}/${v}`)).json()) as EventBucket;
      await cache.set(`bucket:${year}`, bucket);
      (out.buckets ??= {})[year] = bucket;
    } catch (e) {
      telemetry("sync_failed", `bucket ${year}: ${String(e)}`, have.dataVersion, have.configVersion);
    }
  }
  return out;
}

/** Device clock error, used for "today" only when it is more than a day off (docs/reliable.md T6). */
function clockOffset(serverTime: string): number {
  const diff = Date.parse(serverTime) - Date.now();
  return Math.abs(diff) > 86_400_000 ? diff : 0;
}
```

Old bucket versions answer `302` to the current one; `fetch` follows the redirect automatically.

### 5.6 Provider: cache first, then sync

The provider loads the cache (or the bundled snapshot) **before** any network call, so the picker
shows immediately. It then syncs on start, when the app returns to the foreground, and every 15
minutes, at most once a minute, backing off after errors.

`lib/calendar/CalendarProvider.tsx`:

```tsx
import { createContext, type ReactNode, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { AppState, useColorScheme } from "react-native";
import bundledData from "../../assets/calendar/data.json";
import { DEFAULT_CONFIG, parseConfig } from "./config";
import { CalendarTable, checkTable, todayEpoch } from "./engine";
import { cache } from "./storage";
import { syncOnce, telemetry } from "./sync";
import type { CalendarData, EventBucket, Occurrence, ThemeTokens, UiConfig } from "./types";

const EVERY = 15 * 60_000;
const MIN_GAP = 60_000;

interface CalendarState {
  ready: boolean;
  table: CalendarTable;
  config: UiConfig;
  configVersion: number;
  tokens: ThemeTokens;
  scheme: "light" | "dark";
  today: number;                              // epoch day
  updateRequired: boolean;
  eventsOn(epoch: number): Occurrence[];
  ensureYear(bsYear: number): void;          // ask the sync to keep this year's events
  refresh(): void;
}

const Ctx = createContext<CalendarState | null>(null);

export function useCalendar() {
  const v = useContext(Ctx);
  if (!v) throw new Error("useCalendar must be used inside <CalendarProvider>");
  return v;
}

export function CalendarProvider({ children }: { children: ReactNode }) {
  const [data, setData] = useState<CalendarData>(bundledData as CalendarData);
  const [config, setConfig] = useState<UiConfig>(DEFAULT_CONFIG);
  const [configVersion, setConfigVersion] = useState(0);
  const [buckets, setBuckets] = useState<Record<number, EventBucket>>({});
  const [updateRequired, setUpdateRequired] = useState(false);
  const [clockOffsetMs, setClockOffsetMs] = useState(0);
  const [ready, setReady] = useState(false);
  const [years, setYears] = useState<number[]>([]);
  const [tick, setTick] = useState(0);           // re-render at midnight / on resume so "today" moves
  const system = useColorScheme();

  const state = useRef({ data, configVersion, buckets, years, running: false, last: 0, failures: 0 });
  state.current = { ...state.current, data, configVersion, buckets, years };

  const table = useMemo(() => new CalendarTable(data), [data]);

  // 1. Load the cache (falls back to the bundled snapshot).
  useEffect(() => {
    (async () => {
      const cached = await cache.get<CalendarData>("data");
      if (cached && cached.version > (bundledData as CalendarData).version && checkTable(cached).length === 0) setData(cached);
      const cfg = await cache.get<{ version: number; config: UiConfig }>("config");
      if (cfg) {
        setConfig(parseConfig(cfg.config).config);
        setConfigVersion(cfg.version);
      }
      const now = todayEpoch("Asia/Kathmandu");
      const current = new CalendarTable(cached ?? (bundledData as CalendarData)).epochToBS(now)?.year;
      const initial = current ? [current - 1, current, current + 1] : [];
      const loaded: Record<number, EventBucket> = {};
      for (const y of initial) {
        const b = await cache.get<EventBucket>(`bucket:${y}`);
        if (b) loaded[y] = b;
      }
      setBuckets(loaded);
      setYears(initial);
      setReady(true);
    })();
  }, []);

  // 2. Sync.
  const sync = useCallback(async (force = false) => {
    const s = state.current;
    const backoff = Math.min(2 ** s.failures * MIN_GAP, EVERY);
    if (s.running || (!force && Date.now() - s.last < (s.failures ? backoff : MIN_GAP))) return;
    s.running = true;
    s.last = Date.now();
    try {
      const r = await syncOnce({
        dataVersion: s.data.version,
        configVersion: s.configVersion,
        bucketVersions: Object.fromEntries(Object.entries(s.buckets).map(([y, b]) => [y, b.version])),
        neededYears: s.years,
      });
      if (r.data) setData(r.data);
      if (r.config) {
        setConfig(r.config);
        setConfigVersion(r.configVersion!);
      }
      if (r.buckets) setBuckets((prev) => ({ ...prev, ...r.buckets }));
      if (r.updateRequired !== undefined) setUpdateRequired(r.updateRequired);
      if (r.clockOffsetMs !== undefined) setClockOffsetMs(r.clockOffsetMs);
      s.failures = 0;
    } catch (e) {
      s.failures += 1;
      telemetry("sync_failed", String(e), s.data.version, s.configVersion);
    } finally {
      s.running = false;
    }
  }, []);

  useEffect(() => {
    if (!ready) return;
    sync(true);
    const timer = setInterval(() => { sync(); setTick((t) => t + 1); }, EVERY);
    const sub = AppState.addEventListener("change", (st) => {
      if (st === "active") { sync(); setTick((t) => t + 1); }
    });
    return () => { clearInterval(timer); sub.remove(); };
  }, [ready, sync]);

  // A newly browsed year: load it from the cache now, then let the sync refresh it.
  const ensureYear = useCallback((bsYear: number) => {
    if (state.current.years.includes(bsYear)) return;
    setYears((ys) => [...ys, bsYear]);
    cache.get<EventBucket>(`bucket:${bsYear}`).then((b) => b && setBuckets((prev) => ({ ...prev, [bsYear]: b })));
    setTimeout(() => sync(true), 0);
  }, [sync]);

  // Index occurrences by epoch day (multi-day events appear on every day they cover).
  const byDay = useMemo(() => {
    const map = new Map<number, Occurrence[]>();
    for (const b of Object.values(buckets)) {
      for (const o of b.events) {
        const [sy, sm, sd] = o.start.ad.split("-").map(Number);
        const [ey, em, ed] = o.end.ad.split("-").map(Number);
        const s = table.toEpoch("AD", { year: sy!, month: sm!, day: sd! });
        const e = table.toEpoch("AD", { year: ey!, month: em!, day: ed! });
        if (s === null || e === null) continue;
        for (let n = s; n <= Math.min(e, s + 366); n++) {
          const list = map.get(n) ?? [];
          if (!list.some((x) => x.id === o.id)) list.push(o);
          map.set(n, list);
        }
      }
    }
    return map;
  }, [buckets, table]);

  const scheme: "light" | "dark" =
    config.defaults.colorScheme === "system" ? (system === "dark" ? "dark" : "light") : config.defaults.colorScheme;

  const value = useMemo<CalendarState>(() => ({
    ready,
    table,
    config,
    configVersion,
    tokens: config.theme[scheme],
    scheme,
    today: todayEpoch(config.defaults.todayTimeZone, clockOffsetMs),
    updateRequired,
    eventsOn: (n) => byDay.get(n) ?? [],
    ensureYear,
    refresh: () => sync(true),
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }), [ready, table, config, configVersion, scheme, clockOffsetMs, updateRequired, byDay, ensureYear, sync, tick]);

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
```

### 5.7 The date picker

A bottom sheet. Everything it shows comes from the provider: the grid from the on-device engine, the
colours and options from the admin config, the dots from the event buckets.

`components/DatePicker.tsx`:

```tsx
import { useEffect, useMemo, useState } from "react";
import { Modal, Pressable, StyleSheet, Text, View } from "react-native";
import { useCalendar } from "../lib/calendar/CalendarProvider";
import { addMonths, fmt, MONTHS, parse, toDevanagari, WEEKDAYS } from "../lib/calendar/engine";
import type { Basis } from "../lib/calendar/types";

export type PickedDate = { ad: string; bs: string };

type Props = {
  visible: boolean;
  value: PickedDate | null;
  onChange: (value: PickedDate) => void;
  onClose: () => void;
  minDate?: string;          // AD "YYYY-MM-DD", optional
  maxDate?: string;          // AD "YYYY-MM-DD", optional
  title?: string;
};

export function DatePicker({ visible, value, onChange, onClose, minDate, maxDate, title }: Props) {
  const cal = useCalendar();
  const { table, config, tokens: t, today } = cal;
  const { defaults, display } = config;
  const [mode, setMode] = useState<Basis>(defaults.mode);
  const [ym, setYm] = useState<{ year: number; month: number } | null>(null);

  const locale = defaults.locale;
  const num = (n: number | string) => (defaults.digits === "devanagari" ? toDevanagari(n) : String(n));

  // Open on the selected date's month, or today's.
  useEffect(() => {
    if (!visible) return;
    setMode(defaults.mode);
    const anchor = value ? (defaults.mode === "BS" ? value.bs : value.ad) : null;
    const todayDate = table.convert(today);
    const start = anchor ?? (defaults.mode === "BS" ? todayDate.bs : todayDate.ad);
    if (start) setYm(parse(start));
  }, [visible]); // eslint-disable-line react-hooks/exhaustive-deps

  // Keep the events of the visible BS year(s).
  useEffect(() => {
    if (!ym) return;
    if (mode === "BS") cal.ensureYear(ym.year);
    else {
      const first = table.toEpoch("AD", { ...ym, day: 1 });
      const bs = first !== null ? table.epochToBS(first) : null;
      if (bs) { cal.ensureYear(bs.year); cal.ensureYear(bs.year + 1); }
    }
  }, [ym, mode]); // eslint-disable-line react-hooks/exhaustive-deps

  const cells = useMemo(() => (ym ? table.monthGrid(mode, ym.year, ym.month, defaults.weekStart) : null), [table, mode, ym, defaults.weekStart]);

  const minEpoch = Math.max(table.minEpoch, minDate ? table.toEpoch("AD", parse(minDate)) ?? table.minEpoch : table.minEpoch);
  const maxEpoch = Math.min(table.maxEpoch, maxDate ? table.toEpoch("AD", parse(maxDate)) ?? table.maxEpoch : table.maxEpoch);

  const canGo = (delta: number) => {
    if (!ym) return false;
    const next = addMonths(ym.year, ym.month, delta);
    return table.hasMonth(mode, next.year, next.month);
  };
  const go = (delta: number) => ym && canGo(delta) && setYm(addMonths(ym.year, ym.month, delta));

  function switchMode(next: Basis) {
    if (next === mode) return;
    // Stay on the same days: jump to the month holding the selected (or first visible) day.
    const anchor = value?.ad ?? cells?.find((c) => c.inMonth)?.ad;
    setMode(next);
    if (anchor) {
      const n = table.toEpoch("AD", parse(anchor));
      const d = n !== null ? table.convert(n) : null;
      const target = d && (next === "BS" ? d.bs : d.ad);
      if (target) setYm(parse(target));
    }
  }

  const weekdays = [...Array(7)].map((_, i) => (defaults.weekStart + i) % 7);
  const status = mode === "BS" && ym ? table.status(ym.year) : null;

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={onClose}>
      <Pressable style={s.backdrop} onPress={onClose} accessibilityLabel="Close date picker" />
      <View style={[s.sheet, { backgroundColor: t.bg, borderTopLeftRadius: config.shape.radius * 1.5, borderTopRightRadius: config.shape.radius * 1.5 }]}>
        <View style={[s.grabber, { backgroundColor: t.border }]} />
        {title ? <Text style={[s.sheetTitle, { color: t.muted }]}>{title}</Text> : null}

        <View style={s.header}>
          <Pressable onPress={() => go(-1)} disabled={!canGo(-1)} hitSlop={12} accessibilityLabel="Previous month">
            <Text style={[s.arrow, { color: canGo(-1) ? t.text : t.disabled }]}>‹</Text>
          </Pressable>
          <Text style={[s.title, { color: t.text }]}>
            {ym ? `${MONTHS[mode][locale][ym.month - 1]} ${num(ym.year)}` : "…"}
          </Text>
          <Pressable onPress={() => go(1)} disabled={!canGo(1)} hitSlop={12} accessibilityLabel="Next month">
            <Text style={[s.arrow, { color: canGo(1) ? t.text : t.disabled }]}>›</Text>
          </Pressable>
        </View>

        {display.allowModeSwitch && (
          <View style={[s.modes, { borderColor: t.border, borderRadius: config.shape.radius }]}>
            {(["BS", "AD"] as const).map((m) => (
              <Pressable key={m} onPress={() => switchMode(m)} accessibilityRole="radio" accessibilityState={{ checked: mode === m }}
                style={[s.mode, mode === m && { backgroundColor: t.primary }]}>
                <Text style={{ color: mode === m ? t.onPrimary : t.text, fontWeight: "600" }}>{m}</Text>
              </Pressable>
            ))}
          </View>
        )}

        {display.showProjectedWarning && status === "projected" && (
          <Text style={[s.note, { color: t.muted }]}>
            {locale === "ne" ? "यो वर्षका मितिहरू अनुमानित हुन्।" : "Dates in this year are projected and may change."}
          </Text>
        )}

        <View style={s.row}>
          {weekdays.map((w) => (
            <Text key={w} style={[s.weekday, { color: defaults.weekendDays.includes(w) ? t.weekend : t.muted }]}>
              {WEEKDAYS[locale][w]}
            </Text>
          ))}
        </View>

        <View style={s.grid}>
          {cells?.map((c) => {
            const events = cal.eventsOn(c.epoch);
            const holiday = display.highlightHolidays && events.some((e) => e.isHoliday);
            const weekend = defaults.weekendDays.includes(c.weekday);
            const disabled = !c.bs || c.epoch < minEpoch || c.epoch > maxEpoch;
            const selected = value?.ad === c.ad;
            const isToday = c.epoch === today;
            const secondary = mode === "BS" ? Number(c.ad.slice(8)) : c.bs ? Number(c.bs.slice(8)) : null;
            const color = selected ? t.onPrimary : disabled ? t.disabled : holiday ? t.holiday : weekend ? t.weekend : t.text;
            const label = `${c.bs ? `BS ${c.bs}, ` : ""}AD ${c.ad}${events.length ? `, ${events.map((e) => e.title.en).join(", ")}` : ""}`;
            return (
              <Pressable
                key={c.ad}
                disabled={disabled}
                onPress={() => {
                  if (!c.bs) return;
                  onChange({ ad: c.ad, bs: c.bs });
                  onClose();
                }}
                accessibilityRole="button"
                accessibilityState={{ selected, disabled }}
                accessibilityLabel={label}
                style={s.cellWrap}
              >
                <View style={[
                  s.cell,
                  { borderRadius: config.shape.radius * 0.75, opacity: c.inMonth ? 1 : 0.35 },
                  holiday && !selected && { backgroundColor: t.surface },
                  isToday && !selected && { borderWidth: 2, borderColor: t.today },
                  selected && { backgroundColor: t.primary },
                ]}>
                  <Text style={[s.day, { color }]}>{c.day ? num(c.day) : ""}</Text>
                  {display.showSecondaryDate && secondary !== null && (
                    <Text style={[s.secondary, { color: selected ? t.onPrimary : t.muted }]}>{num(secondary)}</Text>
                  )}
                  {display.showEventDots && events.length > 0 && (
                    <View style={s.dots}>
                      {events.slice(0, display.maxDotsPerDay).map((e) => (
                        <View key={e.id} style={[s.dot, { backgroundColor: selected ? t.onPrimary : e.color[cal.scheme] }]} />
                      ))}
                    </View>
                  )}
                </View>
              </Pressable>
            );
          })}
        </View>

        <View style={s.footer}>
          <Pressable
            onPress={() => {
              const d = table.convert(today);
              if (d.bs) setYm(parse(mode === "BS" ? d.bs : d.ad));
            }}
            hitSlop={8}
          >
            <Text style={{ color: t.primary, fontWeight: "600" }}>{locale === "ne" ? "आज" : "Today"}</Text>
          </Pressable>
          <Pressable onPress={onClose} hitSlop={8}>
            <Text style={{ color: t.muted }}>{locale === "ne" ? "बन्द" : "Close"}</Text>
          </Pressable>
        </View>
      </View>
    </Modal>
  );
}

const s = StyleSheet.create({
  backdrop: { flex: 1, backgroundColor: "rgba(0,0,0,0.4)" },
  sheet: { paddingHorizontal: 12, paddingTop: 8, paddingBottom: 28 },
  grabber: { alignSelf: "center", width: 40, height: 4, borderRadius: 2, marginBottom: 8 },
  sheetTitle: { textAlign: "center", fontSize: 13, marginBottom: 4 },
  header: { flexDirection: "row", alignItems: "center", justifyContent: "space-between", paddingHorizontal: 8 },
  arrow: { fontSize: 30, paddingHorizontal: 10 },
  title: { fontSize: 18, fontWeight: "700" },
  modes: { flexDirection: "row", alignSelf: "center", borderWidth: 1, overflow: "hidden", marginVertical: 10 },
  mode: { paddingHorizontal: 20, paddingVertical: 6 },
  note: { textAlign: "center", fontSize: 12, marginBottom: 6 },
  row: { flexDirection: "row" },
  weekday: { width: `${100 / 7}%`, textAlign: "center", fontSize: 12, fontWeight: "600", paddingVertical: 4 },
  grid: { flexDirection: "row", flexWrap: "wrap" },
  cellWrap: { width: `${100 / 7}%`, aspectRatio: 1, padding: 2 },
  cell: { flex: 1, alignItems: "center", justifyContent: "center", borderWidth: 0, borderColor: "transparent" },
  day: { fontSize: 16, fontWeight: "600" },
  secondary: { fontSize: 10 },
  dots: { flexDirection: "row", gap: 2, marginTop: 2, height: 5 },
  dot: { width: 5, height: 5, borderRadius: 3 },
  footer: { flexDirection: "row", justifyContent: "space-between", paddingHorizontal: 12, paddingTop: 12 },
});
```

### 5.8 A date field

`components/DateField.tsx`:

```tsx
import { useState } from "react";
import { Pressable, Text, View } from "react-native";
import { useCalendar } from "../lib/calendar/CalendarProvider";
import { MONTHS, parse, toDevanagari } from "../lib/calendar/engine";
import { DatePicker, type PickedDate } from "./DatePicker";

type Props = { label: string; value: PickedDate | null; onChange: (v: PickedDate) => void; minDate?: string; maxDate?: string };

export function DateField({ label, value, onChange, minDate, maxDate }: Props) {
  const { tokens: t, config } = useCalendar();
  const [open, setOpen] = useState(false);
  const { locale, digits } = config.defaults;
  const num = (n: number) => (digits === "devanagari" ? toDevanagari(n) : String(n));

  let text = locale === "ne" ? "मिति छान्नुहोस्" : "Choose a date";
  if (value) {
    const bs = parse(value.bs);
    const ad = parse(value.ad);
    text = `${MONTHS.BS[locale][bs.month - 1]} ${num(bs.day)}, ${num(bs.year)}  ·  ${ad.day} ${MONTHS.AD.en[ad.month - 1].slice(0, 3)} ${ad.year}`;
  }

  return (
    <View style={{ gap: 6 }}>
      <Text style={{ color: t.muted, fontSize: 13 }}>{label}</Text>
      <Pressable onPress={() => setOpen(true)} accessibilityRole="button" accessibilityHint="Opens the date picker"
        style={{ borderWidth: 1, borderColor: t.border, borderRadius: config.shape.radius, padding: 14, backgroundColor: t.surface }}>
        <Text style={{ color: value ? t.text : t.muted, fontSize: 16 }}>{text}</Text>
      </Pressable>
      <DatePicker visible={open} value={value} onChange={onChange} onClose={() => setOpen(false)}
        minDate={minDate} maxDate={maxDate} title={label} />
    </View>
  );
}
```

## 6. Using the picker in screens

Wrap the app once, then use `DateField` (or `DatePicker` directly) anywhere.

`App.tsx` (or `app/_layout.tsx` with Expo Router):

```tsx
import { SafeAreaView, Text, View } from "react-native";
import { useState } from "react";
import { CalendarProvider, useCalendar } from "./lib/calendar/CalendarProvider";
import { DateField } from "./components/DateField";
import type { PickedDate } from "./components/DatePicker";

export default function App() {
  return (
    <CalendarProvider>
      <BookingScreen />
    </CalendarProvider>
  );
}

function BookingScreen() {
  const { tokens: t, updateRequired, table, today } = useCalendar();
  const [date, setDate] = useState<PickedDate | null>(null);
  const todayAd = table.convert(today).ad;

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: t.bg }}>
      <View style={{ padding: 16, gap: 16 }}>
        {updateRequired && (
          <Text style={{ color: t.holiday }}>A new version of the app is available. Please update.</Text>
        )}
        <DateField label="Appointment date" value={date} onChange={setDate} minDate={todayAd} />
        {date && <Text style={{ color: t.text }}>Saved: BS {date.bs} = AD {date.ad}</Text>}
      </View>
    </SafeAreaView>
  );
}
```

**Store and send dates as strings.** Keep `{ ad: "2026-09-24", bs: "2083-06-08" }` as they are. Don't
convert them to JavaScript `Date`: that adds a time and time zone and can move the day.

**Other things the provider gives you:**

```ts
const { table, eventsOn, today } = useCalendar();
table.convert(today);                                  // { ad, bs, weekday } for today
table.bsToEpoch({ year: 2083, month: 6, day: 8 });     // epoch day, or null if the date doesn't exist
table.monthGrid("BS", 2083, 6);                        // 42 cells for your own calendar screen
eventsOn(table.bsToEpoch({ year: 2083, month: 6, day: 25 })!);   // holidays and events that day
```

## 7. How an admin change reaches phones

Example: an admin changes the primary colour and publishes it to 20 % of phones first.

1. **Dashboard.** A designer edits the mobile theme. The server checks every field and the colour
   contrast (WCAG AA) and refuses unreadable colours.
2. **Review.** A second designer (or a super admin) approves it with a rollout of 20 %. The server
   creates a new immutable config version, for example `/v1/ui-config/mobile/8`.
3. **Manifest.** `config` now says `{ version: 7, candidate: { version: 8, percent: 20 } }`.
4. **Phones.** At the next sync (app start, return to foreground, or the 15-minute timer), each phone
   computes `fnv1a32(installId) % 100`. Phones below 20 download version 8; the rest keep 7. The same
   phone always gets the same answer, so users don't flip between themes.
5. **Promote or roll back.** Moving to 100 % makes version 8 the stable one for everyone. A rollback
   publishes a new version with the old content, so every phone switches back at its next sync.

Events and year-table corrections work the same way: publishing an event bumps that BS year's bucket
version; an approved table correction bumps `dataVersion`. Phones download only the changed file.

**How fast?** Up to 30 seconds of server-side caching, plus the time until the phone's next sync. While
the app is open that is at most 15 minutes; a user returning to the app gets it right away.
`provider.refresh()` forces a sync, for example on pull-to-refresh.

**Forcing an app update.** When an admin raises `minSupportedClient` above the app's `version` in
`app.json`, the manifest returns `updateRequired: true` and the example above shows a message. The
calendar keeps working from the cache either way.

## 8. Shipping code changes without store review (EAS Update)

Section 7 covers **data and theme**. To change the picker's **code** (a new layout, a bug fix) without
waiting for store review, use [EAS Update](https://docs.expo.dev/eas-update/introduction/). It
downloads a new JavaScript bundle and assets the next time the app starts.

Both Apple (Developer Program License Agreement, on interpreted code) and Google Play allow downloading interpreted code such
as a JavaScript bundle, provided the update does not change the app's main purpose or add features that
would need review. Changes that touch native code cannot be sent this way.

### 8.1 Set up once

```bash
npm install -g eas-cli
eas login
npx expo install expo-updates
eas update:configure                 # adds updates.url and runtimeVersion to app.json
```

`app.json` (the relevant part):

```json
{
  "expo": {
    "version": "1.4.0",
    "runtimeVersion": { "policy": "appVersion" },
    "updates": { "url": "https://u.expo.dev/<your-project-id>", "checkAutomatically": "ON_LOAD" }
  }
}
```

`runtimeVersion` ties an update to the native build it can run on. With `appVersion`, an update made
for 1.4.0 is only delivered to installs of 1.4.0. Bump `version` whenever you change native code and
make a new store build.

`eas.json`, one channel per environment:

```json
{
  "build": {
    "preview": { "channel": "preview", "distribution": "internal" },
    "production": { "channel": "production" }
  }
}
```

Build and submit to the stores once:

```bash
eas build --profile production --platform all
eas submit --platform all
```

### 8.2 Publish a code change

```bash
# 1. Try it on internal builds first
eas update --branch preview --message "Picker: bigger day cells"

# 2. Then send it to users
eas update --branch production --message "Picker: bigger day cells"

# Roll back if needed: re-publish a previous update
eas update:republish --group <update-group-id> --branch production
```

Phones download the update in the background on launch and use it on the next launch. To apply it
sooner, check when the app starts:

```tsx
import * as Updates from "expo-updates";
import { useEffect } from "react";

export function useApplyUpdates() {
  useEffect(() => {
    if (__DEV__) return;
    (async () => {
      try {
        const check = await Updates.checkForUpdateAsync();
        if (check.isAvailable) {
          await Updates.fetchUpdateAsync();
          await Updates.reloadAsync();          // restart into the new bundle
        }
      } catch {
        // offline or no update server: keep running the current bundle
      }
    })();
  }, []);
}
```

### 8.3 Which route for which change

| Change | Route | Store review? |
|--------|-------|---------------|
| Colours, dark mode, default BS/AD, language, digits, week start, weekend, what the picker shows | Admin dashboard → UI config | No |
| Holidays, events, category colours | Admin dashboard → events | No |
| BS month-length corrections | Admin dashboard → year table (two admins) | No |
| Picker layout, new screens, JavaScript bug fixes, new images | `eas update` | No |
| New native module, permission, SDK upgrade, app icon, splash screen | `eas build` + store submit | Yes |

Bare React Native without Expo can use `expo-updates` too (after `npx install-expo-modules`), or a
self-hosted update server that follows the Expo Updates protocol.

## 9. Testing checklist

- [ ] **Airplane mode on first launch:** the picker opens and converts (bundled snapshot).
- [ ] **Theme change:** change the primary colour in the dashboard, background the app, bring it back:
      the new colour appears.
- [ ] **Staged rollout:** approve at 50 %. On two devices, one may change and one may not. The same device
      always gets the same result. Roll back: both return to the old theme.
- [ ] **Event:** publish a holiday in the current month. After the next sync its dot appears and the day
      is highlighted.
- [ ] **Dark mode:** switch the phone to dark: the picker uses the dark palette (with `"colorScheme": "system"`).
- [ ] **Nepali:** set language `ne` and digits `devanagari`: months, weekdays and numbers are Nepali.
- [ ] **Range ends:** go to Baisakh 1975 and Chaitra 2100: the arrows stop and there are no errors.
- [ ] **Bad field:** save a config with one invalid colour through the API: only that colour falls back
      (a `config_field_fallback` event arrives at `/v1/telemetry`).
- [ ] **Update required:** raise `minSupportedClient` above the app version: the message shows and the
      calendar still works.
- [ ] **Conversion:** spot-check a few dates against `GET /v1/convert` (for example BS 2083-01-01 = AD 2026-04-14).

## 10. Common problems

| Symptom | Cause and fix |
|---------|---------------|
| `Network request failed` on the Android emulator | Use `http://10.0.2.2:8080`, not `localhost`. |
| Works in development, fails in the store build | Release builds require `https`. Point `EXPO_PUBLIC_CAL_API` at the production HTTPS URL, then rebuild (these variables are baked in at build time). |
| `401 INVALID_API_KEY` | Wrong key, or `.env` changed without restarting Metro (`npx expo start -c`). |
| `403 ORIGIN_NOT_ALLOWED` | You used the website's key. Mobile apps need a key without `allowedOrigins`. |
| Theme never changes | The app never syncs: check that `CalendarProvider` wraps the app, and that `EXPO_PUBLIC_CAL_APP` matches the app key the admin edits (`mobile` by default). |
| Theme ignores dark mode | Set `"userInterfaceStyle": "automatic"` in `app.json`. |
| Some phones have the new theme, others don't | A staged rollout is running. Check `config.candidate` in `GET /v1/manifest?app=mobile`. |
| Events missing for a far year | The provider keeps the current year ±1 and any year opened in the picker; the first time a far year is opened, its events arrive after a sync. |
| `Cannot find module '../../assets/calendar/data.json'` | Run the snapshot script (section 4.3). |
| Nepali digits show as boxes | The device font lacks Devanagari. Bundle Noto Sans Devanagari with `expo-font`. |
| An EAS update doesn't arrive | The update's `runtimeVersion` doesn't match the installed build, or it was published to a different branch than the build's channel. |
