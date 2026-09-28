# Using the calendar in an Expo app (React Native)

This guide builds an **AD/BS date picker** (bottom sheet) and an **event calendar** for an Expo app, driven
entirely by the API. No extra packages and no native modules: only `fetch`, React and React Native, so it
runs in Expo Go. It reuses the two shared files from the [web guide](web.md) unchanged.

Need the picker to work with **no network at all**, from the first launch, with admin-controlled rollouts?
Use the complete offline setup in [react-native.md](react-native.md) instead. This guide is the quick,
online version.

**Before you start**

1. The API is running: your own (`docker compose up -d`, see [deployment.md](deployment.md)) or the
   hosted instance at `https://calendar.oneclickinfosys.com/api` (its server setup is public too:
   [bs-calendar-server](https://github.com/Sarojdhakal307/bs-calendar-server)).
2. You have a **public API key** for the app. In development use `pk_dev_local_public_key_0001`. In
   production an admin issues one. Mobile apps send no `Origin` header, so the key must **not** have
   `allowedOrigins`:
   ```bash
   curl -X POST https://calendar-api.example.org/v1/admin/api-clients -H "Authorization: Bearer $TOKEN" \
     -H 'Content-Type: application/json' -d '{"name":"Mobile app","kind":"public"}'
   ```
3. CORS does not apply to mobile apps, so `CORS_ALLOWED_ORIGINS` needs no change.

**How the pieces fit**

| Part of the UI | API call |
|----------------|----------|
| The month grid (42 day cells, both calendars, today, weekend, holidays, events) | `GET /v1/months/{AD\|BS}/{year}/{month}?include=events` |
| The starting month when nothing is selected | `GET /v1/today` |
| Colours, default AD/BS mode, language (set by admins) | `GET /v1/manifest?app=mobile` → `GET {links.config}` |
| A date typed by the user | `GET /v1/convert?bs=2083-06-08` |

The picker never converts dates itself: every cell already contains both the AD and the BS date.

---

## 1. The API client (shared with the web guide)

Copy `lib/calendar-api.ts` from [web.md §1](web.md#1-the-api-client-shared-by-web-and-react-native)
unchanged. Then create the client once, in `lib/api.ts`:

```ts
import { createCalendarApi } from "./calendar-api";

export const calendarApi = createCalendarApi(process.env.EXPO_PUBLIC_CAL_API!, process.env.EXPO_PUBLIC_CAL_KEY!);
```

## 2. Two hooks (shared with the web guide)

Copy `lib/use-calendar.ts` from [web.md §2](web.md#2-two-hooks-shared-by-web-and-react-native) unchanged.
It gives you `useMonth(api, basis, year, month)` and `useUiConfig(api, app)`.

## 3. The date picker component

`components/DatePickerSheet.tsx`, a bottom sheet with the month grid:

```tsx
import { useEffect, useState } from "react";
import { ActivityIndicator, Modal, Pressable, StyleSheet, Text, View } from "react-native";
import { type Basis, type CalendarApi, type ThemeTokens, WEEKDAYS, addMonths, toNepaliDigits, yearMonthOf } from "../lib/calendar-api";
import { useMonth } from "../lib/use-calendar";

export type DateValue = { ad: string; bs: string };

type Props = {
  api: CalendarApi;
  visible: boolean;
  onClose: () => void;
  value: DateValue | null;
  onChange: (value: DateValue) => void;
  theme: ThemeTokens;
  radius?: number;
  defaultMode?: Basis;
  locale?: "en" | "ne";
  allowModeSwitch?: boolean;
};

export function DatePickerSheet(props: Props) {
  const { api, visible, onClose, value, onChange, theme, radius = 12, defaultMode = "BS", locale = "en", allowModeSwitch = true } = props;
  const [mode, setMode] = useState<Basis>(defaultMode);
  const [ym, setYm] = useState(value ? yearMonthOf(defaultMode === "BS" ? value.bs : value.ad) : null);
  const { grid, loading, error } = useMonth(api, mode, ym?.year ?? null, ym?.month ?? null);

  // Nothing selected yet: open on the current month.
  useEffect(() => {
    if (visible && !ym) api.today().then((t) => setYm(yearMonthOf(mode === "BS" ? t.bs.date : t.ad.date)));
  }, [api, visible, ym, mode]);

  const digits = (n: number | string) => (locale === "ne" ? toNepaliDigits(n) : String(n));

  // Switching AD/BS keeps the same day on screen: take that day's date in the other calendar.
  function switchMode(next: Basis) {
    const anchor = value ?? grid?.cells.find((c) => c.inMonth);
    const date = anchor ? (next === "AD" ? anchor.ad : anchor.bs) : null;
    setMode(next);
    if (date) setYm(yearMonthOf(date));
  }

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={onClose}>
      <Pressable style={styles.backdrop} onPress={onClose} accessibilityLabel="Close" />
      <View style={[styles.sheet, { backgroundColor: theme.bg, borderTopLeftRadius: radius, borderTopRightRadius: radius }]}>
        <View style={styles.header}>
          <Pressable onPress={() => ym && setYm(addMonths(ym.year, ym.month, -1))} hitSlop={12} accessibilityLabel="Previous month">
            <Text style={[styles.arrow, { color: theme.text }]}>‹</Text>
          </Pressable>
          <Text style={[styles.title, { color: theme.text }]}>
            {grid ? `${grid.monthName[locale]} ${digits(grid.year)}` : "…"}
          </Text>
          <Pressable onPress={() => ym && setYm(addMonths(ym.year, ym.month, 1))} hitSlop={12} accessibilityLabel="Next month">
            <Text style={[styles.arrow, { color: theme.text }]}>›</Text>
          </Pressable>
        </View>

        {allowModeSwitch && (
          <View style={styles.modes} accessibilityRole="radiogroup">
            {(["BS", "AD"] as const).map((m) => (
              <Pressable key={m} onPress={() => switchMode(m)} accessibilityRole="radio" accessibilityState={{ checked: mode === m }}
                style={[styles.mode, { borderColor: theme.border }, mode === m && { backgroundColor: theme.primary }]}>
                <Text style={{ color: mode === m ? theme.onPrimary : theme.text }}>{m}</Text>
              </Pressable>
            ))}
          </View>
        )}

        <View style={styles.row}>
          {WEEKDAYS[locale].map((w) => (
            <Text key={w} style={[styles.weekday, { color: theme.muted }]}>{w}</Text>
          ))}
        </View>

        {error ? <Text style={{ color: theme.holiday, padding: 16 }}>Could not load the calendar ({error}).</Text> : null}
        {loading && !grid ? <ActivityIndicator style={{ padding: 24 }} color={theme.primary} /> : null}

        <View style={styles.row}>
          {grid?.cells.map((c) => {
            const selected = value?.ad === c.ad;
            const red = c.isHoliday || c.isWeekend;
            const secondary = mode === "BS" ? Number(c.ad.slice(8)) : c.bs ? Number(c.bs.slice(8)) : null;
            return (
              <Pressable
                key={c.ad}
                disabled={!c.bs}
                onPress={() => { if (c.bs) { onChange({ ad: c.ad, bs: c.bs }); onClose(); } }}
                accessibilityRole="button"
                accessibilityState={{ selected, disabled: !c.bs }}
                accessibilityLabel={`BS ${c.bs ?? "-"}, AD ${c.ad}`}
                style={[
                  styles.cell,
                  selected && { backgroundColor: theme.primary },
                  c.isToday && !selected && { borderWidth: 2, borderColor: theme.today },
                ]}
              >
                <Text style={{ fontSize: 16, opacity: c.inMonth ? 1 : 0.4, color: selected ? theme.onPrimary : red ? theme.holiday : c.bs ? theme.text : theme.disabled }}>
                  {digits(c.day)}
                </Text>
                {secondary !== null && (
                  <Text style={{ fontSize: 9, color: selected ? theme.onPrimary : theme.muted }}>{digits(secondary)}</Text>
                )}
                {c.eventIds.length > 0 && <View style={[styles.dot, { backgroundColor: selected ? theme.onPrimary : theme.primary }]} />}
              </Pressable>
            );
          })}
        </View>
      </View>
    </Modal>
  );
}

const styles = StyleSheet.create({
  backdrop: { flex: 1, backgroundColor: "rgba(0,0,0,0.4)" },
  sheet: { paddingHorizontal: 12, paddingTop: 12, paddingBottom: 32 },
  header: { flexDirection: "row", alignItems: "center", justifyContent: "space-between", paddingHorizontal: 8 },
  arrow: { fontSize: 28, paddingHorizontal: 8 },
  title: { fontSize: 18, fontWeight: "600" },
  modes: { flexDirection: "row", gap: 8, justifyContent: "center", marginVertical: 12 },
  mode: { borderWidth: 1, borderRadius: 8, paddingHorizontal: 16, paddingVertical: 6 },
  row: { flexDirection: "row", flexWrap: "wrap" },
  weekday: { width: "14.2857%", textAlign: "center", fontSize: 12, paddingVertical: 4 },
  cell: { width: "14.2857%", aspectRatio: 1, alignItems: "center", justifyContent: "center", borderRadius: 8 },
  dot: { width: 4, height: 4, borderRadius: 2, marginTop: 2 },
});
```

## 4. The event calendar component

A full-width month view with the selected day's events underneath. It reuses the same hook.

`components/EventCalendar.tsx`:

```tsx
import { useEffect, useState } from "react";
import { Pressable, StyleSheet, Text, View } from "react-native";
import { type Basis, type CalendarApi, type ThemeTokens, WEEKDAYS, addMonths, yearMonthOf } from "../lib/calendar-api";
import { useMonth } from "../lib/use-calendar";

type Props = { api: CalendarApi; theme: ThemeTokens; scheme: "light" | "dark"; mode?: Basis; locale?: "en" | "ne" };

export function EventCalendar({ api, theme, scheme, mode = "BS", locale = "en" }: Props) {
  const [ym, setYm] = useState<{ year: number; month: number } | null>(null);
  const [selectedAd, setSelectedAd] = useState<string | null>(null);
  const { grid } = useMonth(api, mode, ym?.year ?? null, ym?.month ?? null);

  useEffect(() => {
    api.today().then((t) => {
      setYm(yearMonthOf(mode === "BS" ? t.bs.date : t.ad.date));
      setSelectedAd(t.ad.date);
    });
  }, [api, mode]);

  const day = selectedAd ?? "";
  const dayEvents = grid?.events.filter((e) => e.start.ad <= day && e.end.ad >= day) ?? [];

  return (
    <View>
      <View style={styles.header}>
        <Pressable onPress={() => ym && setYm(addMonths(ym.year, ym.month, -1))} hitSlop={12}>
          <Text style={[styles.arrow, { color: theme.text }]}>‹</Text>
        </Pressable>
        <Text style={[styles.title, { color: theme.text }]}>{grid ? `${grid.monthName[locale]} ${grid.year}` : "…"}</Text>
        <Pressable onPress={() => ym && setYm(addMonths(ym.year, ym.month, 1))} hitSlop={12}>
          <Text style={[styles.arrow, { color: theme.text }]}>›</Text>
        </Pressable>
      </View>

      <View style={styles.row}>
        {WEEKDAYS[locale].map((w) => <Text key={w} style={[styles.weekday, { color: theme.muted }]}>{w}</Text>)}
        {grid?.cells.map((c) => {
          const selected = selectedAd === c.ad;
          return (
            <Pressable key={c.ad} onPress={() => setSelectedAd(c.ad)}
              style={[styles.cell, selected && { backgroundColor: theme.primary }, c.isToday && !selected && { borderWidth: 2, borderColor: theme.today }]}>
              <Text style={{ opacity: c.inMonth ? 1 : 0.4, color: selected ? theme.onPrimary : c.isHoliday ? theme.holiday : theme.text }}>{c.day}</Text>
              {c.eventIds.length > 0 && <View style={[styles.dot, { backgroundColor: selected ? theme.onPrimary : theme.primary }]} />}
            </Pressable>
          );
        })}
      </View>

      <View style={{ marginTop: 12 }}>
        {dayEvents.map((e) => (
          <View key={e.id} style={[styles.event, { borderLeftColor: e.color[scheme] }]}>
            <Text style={{ color: e.isHoliday ? theme.holiday : theme.text }}>
              {e.title[locale] ?? e.title.en} {e.isHoliday ? "(holiday)" : ""}
            </Text>
          </View>
        ))}
        {dayEvents.length === 0 && <Text style={{ color: theme.muted }}>No events</Text>}
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  header: { flexDirection: "row", alignItems: "center", justifyContent: "space-between" },
  arrow: { fontSize: 28, paddingHorizontal: 8 },
  title: { fontSize: 18, fontWeight: "600" },
  row: { flexDirection: "row", flexWrap: "wrap" },
  weekday: { width: "14.2857%", textAlign: "center", fontSize: 12, paddingVertical: 4 },
  cell: { width: "14.2857%", aspectRatio: 1, alignItems: "center", justifyContent: "center", borderRadius: 8 },
  dot: { width: 4, height: 4, borderRadius: 2, marginTop: 2 },
  event: { borderLeftWidth: 4, paddingLeft: 8, marginBottom: 8 },
});
```

## 5. Admin-controlled theme, light and dark

Load the config once and combine it with the phone's appearance. `lib/use-theme.ts`:

```ts
import { useColorScheme } from "react-native";
import type { CalendarApi, ThemeTokens } from "./calendar-api";
import { useUiConfig } from "./use-calendar";

// Used until the admin config has loaded (same values as the server's default config).
const FALLBACK: Record<"light" | "dark", ThemeTokens> = {
  light: { bg: "#FFFFFF", surface: "#F6F7F9", text: "#111827", muted: "#4B5563", border: "#E5E7EB", primary: "#1D4ED8",
    onPrimary: "#FFFFFF", today: "#1D4ED8", holiday: "#B91C1C", weekend: "#B91C1C", disabled: "#9CA3AF" },
  dark: { bg: "#0B0F17", surface: "#141A24", text: "#F3F4F6", muted: "#9CA3AF", border: "#273041", primary: "#60A5FA",
    onPrimary: "#0B0F17", today: "#60A5FA", holiday: "#F87171", weekend: "#F87171", disabled: "#4B5563" },
};

export function useCalendarTheme(api: CalendarApi) {
  const config = useUiConfig(api, "mobile");
  const system = useColorScheme(); // "light" | "dark" | null
  const pref = config?.defaults.colorScheme ?? "system";
  const scheme = pref === "system" ? (system === "dark" ? "dark" : "light") : pref;
  return {
    scheme,
    tokens: config?.theme[scheme] ?? FALLBACK[scheme],
    radius: config?.shape.radius ?? 12,
    mode: config?.defaults.mode ?? "BS",
    locale: config?.defaults.locale ?? "en",
    allowModeSwitch: config?.display.allowModeSwitch ?? true,
  };
}
```

The theme follows the phone when the user changes its appearance, and admins can change colours or force
light or dark from the dashboard without an app release. Set `"userInterfaceStyle": "automatic"` in
`app.json`, otherwise the phone always reports light.

## 6. Putting it together in Expo (Expo Router)

`.env` (Expo reads `EXPO_PUBLIC_*` variables; restart Metro with `npx expo start -c` after changing it):

```bash
EXPO_PUBLIC_CAL_API=http://192.168.1.20:8080       # see "Which address?" below
EXPO_PUBLIC_CAL_KEY=pk_dev_local_public_key_0001   # public key: it ships inside the app, which is fine
```

**Which address?** The phone or emulator must reach the API:

| Where the app runs | Use |
|--------------------|-----|
| Android emulator | `http://10.0.2.2:8080` (the emulator's name for your computer) |
| iOS simulator | `http://localhost:8080` |
| A real phone on the same Wi-Fi | `http://<your computer's LAN IP>:8080` |
| Production | Your HTTPS address, for example `https://calendar.oneclickinfosys.com/api`. Release builds block plain `http`. |

`lib/use-refresh.ts`: admin changes (new holidays, a new theme) appear when the user returns to the app:

```ts
import { useEffect, useState } from "react";
import { AppState } from "react-native";
import { calendarApi } from "./api";

export function useRefreshOnForeground() {
  const [refreshKey, setRefreshKey] = useState(0);
  useEffect(() => {
    const sub = AppState.addEventListener("change", (state) => {
      if (state === "active") {
        calendarApi.clearCache();
        setRefreshKey((k) => k + 1);
      }
    });
    return () => sub.remove();
  }, []);
  return refreshKey; // use as a React key so the calendar reloads
}
```

`app/index.tsx`:

```tsx
import { useState } from "react";
import { Pressable, ScrollView, Text } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { calendarApi } from "@/lib/api";
import { useCalendarTheme } from "@/lib/use-theme";
import { useRefreshOnForeground } from "@/lib/use-refresh";
import { DatePickerSheet, type DateValue } from "@/components/DatePickerSheet";
import { EventCalendar } from "@/components/EventCalendar";

export default function Home() {
  const theme = useCalendarTheme(calendarApi);
  const refreshKey = useRefreshOnForeground();
  const [open, setOpen] = useState(false);
  const [date, setDate] = useState<DateValue | null>(null);

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: theme.tokens.bg }}>
      <ScrollView key={refreshKey} contentContainerStyle={{ padding: 16, gap: 16 }}>
        <Pressable onPress={() => setOpen(true)}
          style={{ borderWidth: 1, borderColor: theme.tokens.border, borderRadius: theme.radius, padding: 12 }}>
          <Text style={{ color: theme.tokens.text }}>
            {date ? `BS ${date.bs}  ·  AD ${date.ad}` : "Choose a date"}
          </Text>
        </Pressable>

        <EventCalendar api={calendarApi} theme={theme.tokens} scheme={theme.scheme} mode={theme.mode} locale={theme.locale} />
      </ScrollView>

      <DatePickerSheet
        api={calendarApi}
        visible={open}
        onClose={() => setOpen(false)}
        value={date}
        onChange={setDate}
        theme={theme.tokens}
        radius={theme.radius}
        defaultMode={theme.mode}
        locale={theme.locale}
        allowModeSwitch={theme.allowModeSwitch}
      />
    </SafeAreaView>
  );
}
```

Run it with `npx expo start` and open it in Expo Go, an emulator or a simulator.

## 7. Bare React Native and older Expo projects

Everything above works the same. Only these differ:

- **No Expo Router:** put the screen in `App.tsx`, and use relative imports (`./lib/api`) instead of `@/`.
- **Bare React Native:** there are no `EXPO_PUBLIC_*` variables; pass the address and key from your own
  config (for example `react-native-config`) to `createCalendarApi`.
- **Offline:** this guide needs the network for months it has not loaded yet. For a picker that works fully
  offline from the first launch, follow [react-native.md](react-native.md).

## 8. Checklist and common problems

| Symptom | Cause and fix |
|---------|---------------|
| `Network request failed` on Android emulator | Use `http://10.0.2.2:8080`, not `localhost`. |
| Works in development, fails in the store build | Release builds require `https`. Point `EXPO_PUBLIC_CAL_API` at the production HTTPS address. |
| `401 INVALID_API_KEY` | Wrong or revoked key, or `.env` changed without restarting Metro (`npx expo start -c`). |
| `403 ORIGIN_NOT_ALLOWED` | You used the website's key. Mobile apps need a key without `allowedOrigins`. |
| `422 OUT_OF_RANGE` | The user navigated before BS 1975 or after BS 2100 (AD 1918-2044). Disable the arrows at those limits. |
| `429 RATE_LIMITED` | More than 600 requests a minute from one device. The in-memory cache in `calendar-api.ts` normally prevents this. |
| Theme ignores dark mode | Set `"userInterfaceStyle": "automatic"` in `app.json`. |
| Nepali digits show as boxes | The device font lacks Devanagari. Bundle a font such as Noto Sans Devanagari with `expo-font`. |
| Dates look one day off | Never build dates with `new Date("2026-09-24")`; keep the `ad`/`bs` strings from the API as they are. |
