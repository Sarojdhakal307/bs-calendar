# Using the calendar on the web (React / Next.js)

This guide builds an **AD/BS date picker** and an **event calendar** for a website, driven entirely by the
API. No extra packages: only `fetch` and React. The same `calendar-api.ts` file is reused in the
[React Native guide](react-native.md).

**Before you start**

1. The API is running (`docker compose up -d`, see [deployment.md](deployment.md)).
2. You have a **public API key** for the website. In development use `pk_dev_local_public_key_0001`. In
   production an admin issues one, locked to your site's address:
   ```bash
   curl -X POST https://calendar-api.example.org/v1/admin/api-clients -H "Authorization: Bearer $TOKEN" \
     -H 'Content-Type: application/json' \
     -d '{"name":"Website","kind":"public","allowedOrigins":["https://www.example.org"]}'
   ```
3. Your site's address is in the API's `CORS_ALLOWED_ORIGINS` (for example `https://www.example.org`).

**How the pieces fit**

| Part of the UI | API call |
|----------------|----------|
| The month grid (42 day cells, both calendars, today, weekend, holidays, events) | `GET /v1/months/{AD\|BS}/{year}/{month}?include=events` |
| The starting month when nothing is selected | `GET /v1/today` |
| Colours, default AD/BS mode, language (set by admins) | `GET /v1/manifest?app=web` → `GET {links.config}` |
| A date typed by the user | `GET /v1/convert?bs=2083-06-08` |

The picker never converts dates itself: every cell already contains both the AD and the BS date.

---

## 1. The API client (shared by web and React Native)

Create `lib/calendar-api.ts`:

```ts
// Minimal client for the BS/AD calendar API. Works in browsers, Next.js and React Native.
export type Basis = "AD" | "BS";
export type Localized = { en: string; ne: string };

export interface CalendarEvent {
  id: string;
  title: { en: string; ne?: string };
  category: string;
  isHoliday: boolean;
  start: { ad: string; bs: string | null };
  end: { ad: string; bs: string | null };
  allDay: boolean;
  startTime: string | null;
  color: { light: string; dark: string };
}

export interface DayCell {
  ad: string;              // "2026-09-24"
  bs: string | null;       // "2083-06-08" (null only outside BS 1975-2100)
  day: number;             // day number in the grid's calendar
  weekday: number;         // 0 = Sunday
  inMonth: boolean;
  isToday: boolean;
  isWeekend: boolean;
  isHoliday: boolean;
  eventIds: string[];
}

export interface MonthGrid {
  basis: Basis;
  year: number;
  month: number;
  monthName: Localized;
  daysInMonth: number;
  weekStart: number;
  cells: DayCell[];        // always 42
  events: CalendarEvent[];
}

export interface Conversion {
  ad: { date: string; year: number; month: number; day: number; monthName: Localized };
  bs: { date: string; dateNe: string; year: number; month: number; day: number; monthName: Localized };
  weekday: { index: number; name: Localized };
}

export type ThemeTokens = Record<
  "bg" | "surface" | "text" | "muted" | "border" | "primary" | "onPrimary" | "today" | "holiday" | "weekend" | "disabled",
  string
>;

export interface UiConfig {
  defaults: { mode: Basis; locale: "en" | "ne"; digits: "latin" | "devanagari"; weekStart: number; colorScheme: "system" | "light" | "dark" };
  display: { allowModeSwitch: boolean; showSecondaryDate: boolean; showEventDots: boolean; maxDotsPerDay: number };
  theme: { light: ThemeTokens; dark: ThemeTokens };
  shape: { radius: number };
}

export class CalendarApiError extends Error {
  constructor(public status: number, public code: string, message: string) {
    super(message);
  }
}

export function createCalendarApi(baseUrl: string, apiKey: string) {
  // Small in-memory cache: months and the theme change rarely (the server allows 5 min caching).
  const cache = new Map<string, { at: number; value: Promise<unknown> }>();
  const TTL = 5 * 60_000;

  function get<T>(path: string, cacheable = false): Promise<T> {
    const hit = cache.get(path);
    if (cacheable && hit && Date.now() - hit.at < TTL) return hit.value as Promise<T>;
    const value = fetch(baseUrl + path, { headers: { "X-Api-Key": apiKey } }).then(async (res) => {
      const body = await res.json().catch(() => ({}));
      if (!res.ok) throw new CalendarApiError(res.status, body.code ?? `HTTP_${res.status}`, body.detail ?? res.statusText);
      return body as T;
    });
    if (cacheable) {
      cache.set(path, { at: Date.now(), value });
      value.catch(() => cache.delete(path));
    }
    return value;
  }

  return {
    month: (basis: Basis, year: number, month: number, weekStart = 0) =>
      get<MonthGrid>(`/v1/months/${basis}/${year}/${month}?include=events&weekStart=${weekStart}`, true),
    today: () => get<Conversion>("/v1/today"),
    convert: (q: { ad: string } | { bs: string }) => get<Conversion>(`/v1/convert?${new URLSearchParams(q)}`),
    uiConfig: async (app: string) => {
      const manifest = await get<{ links: { config: string } }>(`/v1/manifest?app=${app}`);
      return get<UiConfig>(manifest.links.config, true);
    },
    clearCache: () => cache.clear(),
  };
}

export type CalendarApi = ReturnType<typeof createCalendarApi>;

// ---- small helpers -------------------------------------------------------------------

/** "2083-06-08" → { year: 2083, month: 6 } */
export function yearMonthOf(isoDate: string) {
  const [y, m] = isoDate.split("-").map(Number);
  return { year: y!, month: m! };
}

/** Moves a year/month by `delta` months. */
export function addMonths(year: number, month: number, delta: number) {
  const i = year * 12 + (month - 1) + delta;
  return { year: Math.floor(i / 12), month: (i % 12) + 1 };
}

/** "2083" → "२०८३" */
export function toNepaliDigits(s: string | number) {
  return String(s).replace(/[0-9]/g, (d) => String.fromCharCode(0x0966 + Number(d)));
}

export const WEEKDAYS: Record<"en" | "ne", string[]> = {
  en: ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"],
  ne: ["आइत", "सोम", "मंगल", "बुध", "बिही", "शुक्र", "शनि"],
};
```

## 2. Two hooks (shared by web and React Native)

Create `lib/use-calendar.ts`:

```ts
import { useEffect, useState } from "react";
import type { Basis, CalendarApi, MonthGrid, UiConfig } from "./calendar-api";

/** Loads one month. Pass year = null while the starting month is unknown. */
export function useMonth(api: CalendarApi, basis: Basis, year: number | null, month: number | null, weekStart = 0) {
  const [state, setState] = useState<{ grid: MonthGrid | null; loading: boolean; error: string | null }>({
    grid: null, loading: true, error: null,
  });
  useEffect(() => {
    if (year === null || month === null) return;
    let alive = true;
    setState((s) => ({ ...s, loading: true, error: null }));
    api.month(basis, year, month, weekStart)
      .then((grid) => alive && setState({ grid, loading: false, error: null }))
      .catch((e) => alive && setState((s) => ({ ...s, loading: false, error: e.code ?? String(e) })));
    return () => { alive = false; };
  }, [api, basis, year, month, weekStart]);
  return state;
}

/** The admin-controlled UI config (null until loaded; use your own defaults meanwhile). */
export function useUiConfig(api: CalendarApi, app: string) {
  const [config, setConfig] = useState<UiConfig | null>(null);
  useEffect(() => {
    api.uiConfig(app).then(setConfig).catch(() => setConfig(null));
  }, [api, app]);
  return config;
}
```

## 3. The date picker component

`components/DatePicker.tsx` (add `"use client";` as the first line in Next.js):

```tsx
"use client";
import { useEffect, useState } from "react";
import { type Basis, type CalendarApi, WEEKDAYS, addMonths, toNepaliDigits, yearMonthOf } from "@/lib/calendar-api";
import { useMonth } from "@/lib/use-calendar";
import "./calendar.css";

export type DateValue = { ad: string; bs: string };

export function DatePicker(props: {
  api: CalendarApi;
  value: DateValue | null;
  onChange: (value: DateValue) => void;
  defaultMode?: Basis;
  locale?: "en" | "ne";
  weekStart?: number;
  allowModeSwitch?: boolean;
}) {
  const { api, value, onChange, locale = "en", weekStart = 0, allowModeSwitch = true } = props;
  const [open, setOpen] = useState(false);
  const [mode, setMode] = useState<Basis>(props.defaultMode ?? "BS");
  const [ym, setYm] = useState(value ? yearMonthOf(mode === "BS" ? value.bs : value.ad) : null);
  const { grid, loading, error } = useMonth(api, mode, ym?.year ?? null, ym?.month ?? null, weekStart);

  // Nothing selected yet: open on the current month.
  useEffect(() => {
    if (!ym) api.today().then((t) => setYm(yearMonthOf(mode === "BS" ? t.bs.date : t.ad.date)));
  }, [api, ym, mode]);

  const digits = (n: number | string) => (locale === "ne" ? toNepaliDigits(n) : String(n));
  const title = grid ? `${grid.monthName[locale]} ${digits(grid.year)}` : "…";

  // Switching AD/BS keeps the same day on screen: take that day's date in the other calendar.
  function switchMode(next: Basis) {
    const anchor = value ?? grid?.cells.find((c) => c.inMonth);
    const date = anchor ? (next === "AD" ? anchor.ad : anchor.bs) : null;
    setMode(next);
    if (date) setYm(yearMonthOf(date));
  }

  const label = value ? (mode === "BS" ? `BS ${digits(value.bs)}` : `AD ${value.ad}`) : "Choose a date";

  return (
    <div className="cal">
      <button type="button" className="cal-input" onClick={() => setOpen(!open)} aria-expanded={open}>
        {label}
      </button>

      {open && (
        <div className="cal-popover" role="dialog" aria-label="Choose a date">
          <div className="cal-header">
            <button type="button" aria-label="Previous month" onClick={() => ym && setYm(addMonths(ym.year, ym.month, -1))}>‹</button>
            <strong>{title}</strong>
            <button type="button" aria-label="Next month" onClick={() => ym && setYm(addMonths(ym.year, ym.month, 1))}>›</button>
          </div>

          {allowModeSwitch && (
            <div className="cal-modes" role="radiogroup" aria-label="Calendar">
              {(["BS", "AD"] as const).map((m) => (
                <button key={m} type="button" role="radio" aria-checked={mode === m} onClick={() => switchMode(m)}>{m}</button>
              ))}
            </div>
          )}

          {error && <p className="cal-error">Could not load the calendar ({error}).</p>}

          <div className="cal-grid" role="grid" aria-busy={loading}>
            {Array.from({ length: 7 }, (_, i) => (
              <div key={i} className="cal-weekday" role="columnheader">{WEEKDAYS[locale][(weekStart + i) % 7]}</div>
            ))}
            {grid?.cells.map((c) => {
              const selected = value?.ad === c.ad;
              const secondary = mode === "BS" ? c.ad.slice(8).replace(/^0/, "") : c.bs?.slice(8).replace(/^0/, "");
              return (
                <button
                  key={c.ad}
                  type="button"
                  role="gridcell"
                  aria-selected={selected}
                  aria-label={`BS ${c.bs ?? "-"}, AD ${c.ad}`}
                  disabled={!c.bs}
                  className={[
                    "cal-day", !c.inMonth && "is-outside", c.isToday && "is-today", selected && "is-selected",
                    (c.isHoliday || c.isWeekend) && "is-holiday", c.eventIds.length > 0 && "has-events",
                  ].filter(Boolean).join(" ")}
                  onClick={() => {
                    if (!c.bs) return;
                    onChange({ ad: c.ad, bs: c.bs });
                    setOpen(false);
                  }}
                >
                  <span>{digits(c.day)}</span>
                  <small>{secondary ? digits(secondary) : ""}</small>
                </button>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}
```

`components/calendar.css`, using CSS variables so the admin-controlled theme applies (section 5):

```css
.cal { position: relative; font-family: system-ui, sans-serif; color: var(--cal-text, #111827); }
.cal-input { padding: 8px 12px; border: 1px solid var(--cal-border, #E5E7EB); border-radius: var(--cal-radius, 12px);
  background: var(--cal-bg, #fff); color: inherit; cursor: pointer; }
.cal-popover { position: absolute; z-index: 10; margin-top: 4px; padding: 12px; width: 320px;
  background: var(--cal-bg, #fff); border: 1px solid var(--cal-border, #E5E7EB); border-radius: var(--cal-radius, 12px);
  box-shadow: 0 8px 24px rgb(0 0 0 / .15); }
.cal-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.cal-header button, .cal-modes button { background: none; border: 1px solid var(--cal-border, #E5E7EB);
  border-radius: 8px; padding: 4px 10px; color: inherit; cursor: pointer; }
.cal-modes { display: flex; gap: 4px; margin-bottom: 8px; }
.cal-modes button[aria-checked="true"] { background: var(--cal-primary, #1D4ED8); color: var(--cal-on-primary, #fff); }
.cal-grid { display: grid; grid-template-columns: repeat(7, 1fr); gap: 2px; }
.cal-weekday { text-align: center; font-size: 12px; color: var(--cal-muted, #4B5563); padding: 4px 0; }
.cal-day { position: relative; aspect-ratio: 1; border: 0; border-radius: 8px; background: none; color: inherit;
  display: flex; flex-direction: column; align-items: center; justify-content: center; cursor: pointer; }
.cal-day small { font-size: 9px; color: var(--cal-muted, #4B5563); }
.cal-day:hover { background: var(--cal-surface, #F6F7F9); }
.cal-day.is-outside { opacity: .4; }
.cal-day.is-holiday { color: var(--cal-holiday, #B91C1C); }
.cal-day.is-today { box-shadow: inset 0 0 0 2px var(--cal-today, #1D4ED8); }
.cal-day.is-selected { background: var(--cal-primary, #1D4ED8); color: var(--cal-on-primary, #fff); }
.cal-day.has-events::after { content: ""; position: absolute; bottom: 4px; width: 4px; height: 4px; border-radius: 50%;
  background: var(--cal-primary, #1D4ED8); }
.cal-day:disabled { color: var(--cal-disabled, #9CA3AF); cursor: default; }
.cal-error { color: var(--cal-holiday, #B91C1C); font-size: 13px; }
```

## 4. The event calendar component

A full-width month view with the day's events underneath. It reuses the same hook.

```tsx
"use client";
import { useEffect, useState } from "react";
import { type Basis, type CalendarApi, WEEKDAYS, addMonths, yearMonthOf } from "@/lib/calendar-api";
import { useMonth } from "@/lib/use-calendar";

export function EventCalendar({ api, mode = "BS", locale = "en" }: { api: CalendarApi; mode?: Basis; locale?: "en" | "ne" }) {
  const [ym, setYm] = useState<{ year: number; month: number } | null>(null);
  const [selectedAd, setSelectedAd] = useState<string | null>(null);
  const { grid } = useMonth(api, mode, ym?.year ?? null, ym?.month ?? null);

  useEffect(() => {
    api.today().then((t) => {
      setYm(yearMonthOf(mode === "BS" ? t.bs.date : t.ad.date));
      setSelectedAd(t.ad.date);
    });
  }, [api, mode]);

  const dayEvents = grid?.events.filter((e) => e.start.ad <= (selectedAd ?? "") && e.end.ad >= (selectedAd ?? "")) ?? [];

  return (
    <section className="cal">
      <header className="cal-header">
        <button onClick={() => ym && setYm(addMonths(ym.year, ym.month, -1))}>‹</button>
        <h2>{grid ? `${grid.monthName[locale]} ${grid.year}` : "…"}</h2>
        <button onClick={() => ym && setYm(addMonths(ym.year, ym.month, 1))}>›</button>
      </header>
      <div className="cal-grid">
        {WEEKDAYS[locale].map((w) => <div key={w} className="cal-weekday">{w}</div>)}
        {grid?.cells.map((c) => (
          <button key={c.ad} onClick={() => setSelectedAd(c.ad)}
            className={["cal-day", !c.inMonth && "is-outside", c.isToday && "is-today", selectedAd === c.ad && "is-selected",
              c.isHoliday && "is-holiday", c.eventIds.length > 0 && "has-events"].filter(Boolean).join(" ")}>
            <span>{c.day}</span>
          </button>
        ))}
      </div>
      <ul>
        {dayEvents.map((e) => (
          <li key={e.id} style={{ borderLeft: `4px solid ${e.color.light}`, paddingLeft: 8 }}>
            {e.title[locale] ?? e.title.en} {e.isHoliday && "(holiday)"}
          </li>
        ))}
        {dayEvents.length === 0 && <li>No events</li>}
      </ul>
    </section>
  );
}
```

## 5. Admin-controlled theme, light and dark

Load the config once and turn the palette into CSS variables on a wrapper element:

```tsx
"use client";
import { type CSSProperties, type ReactNode, useEffect, useState } from "react";
import type { CalendarApi, ThemeTokens } from "@/lib/calendar-api";
import { useUiConfig } from "@/lib/use-calendar";

function useSystemDark() {
  const [dark, setDark] = useState(false);
  useEffect(() => {
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    setDark(mq.matches);
    const on = (e: MediaQueryListEvent) => setDark(e.matches);
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, []);
  return dark;
}

const cssVars = (t: ThemeTokens, radius: number) => ({
  "--cal-bg": t.bg, "--cal-surface": t.surface, "--cal-text": t.text, "--cal-muted": t.muted,
  "--cal-border": t.border, "--cal-primary": t.primary, "--cal-on-primary": t.onPrimary,
  "--cal-today": t.today, "--cal-holiday": t.holiday, "--cal-weekend": t.weekend,
  "--cal-disabled": t.disabled, "--cal-radius": `${radius}px`,
}) as CSSProperties;

export function CalendarTheme({ api, children }: { api: CalendarApi; children: ReactNode }) {
  const config = useUiConfig(api, "web");
  const systemDark = useSystemDark();
  if (!config) return <>{children}</>; // CSS fallbacks apply until the config arrives
  const pref = config.defaults.colorScheme;
  const scheme = pref === "system" ? (systemDark ? "dark" : "light") : pref;
  return <div style={cssVars(config.theme[scheme], config.shape.radius)}>{children}</div>;
}
```

`config.defaults` also tells you the admin's preferred starting mode (`mode`), language (`locale`),
digits and week start. Pass them to `<DatePicker>`.

## 6. Putting it together in Next.js (App Router)

`.env.local`:

```bash
NEXT_PUBLIC_CAL_API=http://localhost:8080
NEXT_PUBLIC_CAL_KEY=pk_dev_local_public_key_0001   # public key: it is visible in the browser, which is fine
CAL_SERVER_KEY=sk_dev_local_server_key_0001        # server-only key for server components
```

`lib/api.ts`:

```ts
import { createCalendarApi } from "./calendar-api";
export const calendarApi = createCalendarApi(process.env.NEXT_PUBLIC_CAL_API!, process.env.NEXT_PUBLIC_CAL_KEY!);
```

`app/booking/page.tsx`:

```tsx
"use client";
import { useState } from "react";
import { calendarApi } from "@/lib/api";
import { CalendarTheme } from "@/components/CalendarTheme";
import { DatePicker, type DateValue } from "@/components/DatePicker";
import { EventCalendar } from "@/components/EventCalendar";

export default function Page() {
  const [date, setDate] = useState<DateValue | null>(null);
  return (
    <CalendarTheme api={calendarApi}>
      <DatePicker api={calendarApi} value={date} onChange={setDate} defaultMode="BS" />
      {date && <p>Selected: AD {date.ad} = BS {date.bs}</p>}
      <EventCalendar api={calendarApi} />
    </CalendarTheme>
  );
}
```

**Server-rendered holiday list** (good for SEO). It runs on the server with the server key and is cached
for an hour:

```tsx
// app/holidays/page.tsx (a server component: no "use client")
export const revalidate = 3600;

export default async function Holidays() {
  const headers = { "X-Api-Key": process.env.CAL_SERVER_KEY! };
  const today = await fetch(`${process.env.NEXT_PUBLIC_CAL_API}/v1/today`, { headers }).then((r) => r.json());
  const to = new Date(Date.parse(today.ad.date) + 365 * 86_400_000).toISOString().slice(0, 10);
  const res = await fetch(
    `${process.env.NEXT_PUBLIC_CAL_API}/v1/events?from=${today.ad.date}&to=${to}&category=public_holiday`,
    { headers, next: { revalidate: 3600, tags: ["calendar"] } },
  );
  const { events } = await res.json();
  return (
    <ul>
      {events.map((e: { id: string; title: { en: string }; start: { ad: string; bs: string } }) => (
        <li key={e.id}>{e.start.bs} (AD {e.start.ad}): {e.title.en}</li>
      ))}
    </ul>
  );
}
```

**Instant refresh when admins publish (optional).** Register a webhook
(`POST /v1/admin/webhooks {"url":"https://www.example.org/api/calendar-hook","topics":["events"]}`) and
add a route that checks the signature and refreshes the cached pages. The signature check is in
[api.md §7](api.md#7-webhooks):

```ts
// app/api/calendar-hook/route.ts
import { revalidateTag } from "next/cache";
import crypto from "node:crypto";

export async function POST(req: Request) {
  const raw = await req.text();
  const header = req.headers.get("x-calendar-signature") ?? "";
  const parts = Object.fromEntries(header.split(",").map((p) => p.split("=")));
  const expected = crypto.createHmac("sha256", process.env.CAL_WEBHOOK_SECRET!).update(`${parts.t}.${raw}`).digest("hex");
  const fresh = Math.abs(Date.now() / 1000 - Number(parts.t)) < 300;
  if (!fresh || parts.v1 !== expected) return new Response("bad signature", { status: 400 });
  revalidateTag("calendar"); // check the revalidateTag signature of your Next.js version
  return Response.json({ ok: true });
}
```

## 7. Plain React (Vite, Create React App)

Everything above works the same. Only the environment variables differ
(`import.meta.env.VITE_CAL_API` in Vite) and there is no `"use client"` line or server component.

## 8. Checklist and common problems

| Symptom | Cause and fix |
|---------|---------------|
| Browser console shows a CORS error | Add the site's origin to `CORS_ALLOWED_ORIGINS` on the API. |
| `403 ORIGIN_NOT_ALLOWED` | The key has `allowedOrigins`; add this site's origin to the key, or use a key without the restriction. |
| `401 INVALID_API_KEY` | Wrong or revoked key, or the `X-Api-Key` header is missing. |
| `422 OUT_OF_RANGE` | The user navigated before BS 1975 or after BS 2100. Disable the arrows at those limits. |
| `429 RATE_LIMITED` | More than 600 requests a minute from one visitor. The in-memory cache in `calendar-api.ts` normally prevents this. |
| Dates look one day off | Never build dates with `new Date("2026-09-24")` in local time; use the `ad`/`bs` strings from the API as they are. |
