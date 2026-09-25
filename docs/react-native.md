# Using the calendar in React Native (Expo or bare)

This guide builds an **AD/BS date picker** (bottom sheet) and a **month event list** for a mobile app,
driven by the API. It reuses the two small files from the [web guide](web.md): the API client and
the hooks. No native modules are needed, so it works in Expo Go.

## 1. Setup

```bash
npx create-expo-app@latest my-app
cd my-app
npx expo install @react-native-async-storage/async-storage   # only for the offline cache (section 6)
```

Create `.env` in the app (Expo reads `EXPO_PUBLIC_*` variables):

```bash
EXPO_PUBLIC_CAL_API=http://192.168.1.20:8080        # see "Which address?" below
EXPO_PUBLIC_CAL_KEY=pk_dev_local_public_key_0001    # a public key; in production one issued for the app
```

**Which address?** The phone or emulator must reach the API:

| Where the app runs | Use |
|--------------------|-----|
| Android emulator | `http://10.0.2.2:8080` (the emulator's name for your computer) |
| iOS simulator | `http://localhost:8080` |
| A real phone on the same Wi-Fi | `http://<your computer's LAN IP>:8080` |
| Production | `https://calendar-api.example.org`. Release builds block plain `http` (iOS App Transport Security, Android cleartext rules). |

Mobile apps do not send an `Origin` header, so CORS does not apply. Issue the app its own public key
**without** `allowedOrigins`:

```bash
curl -X POST https://calendar-api.example.org/v1/admin/api-clients -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"Mobile app","kind":"public"}'
```

## 2. Copy the shared files

Copy these two files from the web guide into `lib/`, unchanged:

- `lib/calendar-api.ts`: [web.md §1](web.md#1-the-api-client-shared-by-web-and-react-native)
- `lib/use-calendar.ts`: [web.md §2](web.md#2-two-hooks-shared-by-web-and-react-native)

Then create the client once, in `lib/api.ts`:

```ts
import { createCalendarApi } from "./calendar-api";

export const calendarApi = createCalendarApi(process.env.EXPO_PUBLIC_CAL_API!, process.env.EXPO_PUBLIC_CAL_KEY!);
```

## 3. Theme: admin colours plus the phone's light or dark mode

`lib/use-theme.ts`:

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
  };
}
```

The theme switches automatically when the user changes the phone's appearance, and admins can change
colours or force light or dark without an app release.

## 4. The date picker (bottom sheet)

`components/DatePickerSheet.tsx`:

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
};

export function DatePickerSheet({ api, visible, onClose, value, onChange, theme, radius = 12, defaultMode = "BS", locale = "en" }: Props) {
  const [mode, setMode] = useState<Basis>(defaultMode);
  const [ym, setYm] = useState(value ? yearMonthOf(defaultMode === "BS" ? value.bs : value.ad) : null);
  const { grid, loading, error } = useMonth(api, mode, ym?.year ?? null, ym?.month ?? null);

  useEffect(() => {
    if (visible && !ym) api.today().then((t) => setYm(yearMonthOf(mode === "BS" ? t.bs.date : t.ad.date)));
  }, [api, visible, ym, mode]);

  const d = (n: number | string) => (locale === "ne" ? toNepaliDigits(n) : String(n));

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
            {grid ? `${grid.monthName[locale]} ${d(grid.year)}` : "…"}
          </Text>
          <Pressable onPress={() => ym && setYm(addMonths(ym.year, ym.month, 1))} hitSlop={12} accessibilityLabel="Next month">
            <Text style={[styles.arrow, { color: theme.text }]}>›</Text>
          </Pressable>
        </View>

        <View style={styles.modes}>
          {(["BS", "AD"] as const).map((m) => (
            <Pressable key={m} onPress={() => switchMode(m)} accessibilityRole="radio" accessibilityState={{ checked: mode === m }}
              style={[styles.mode, { borderColor: theme.border }, mode === m && { backgroundColor: theme.primary }]}>
              <Text style={{ color: mode === m ? theme.onPrimary : theme.text }}>{m}</Text>
            </Pressable>
          ))}
        </View>

        <View style={styles.row}>
          {WEEKDAYS[locale].map((w) => (
            <Text key={w} style={[styles.weekday, { color: theme.muted }]}>{w}</Text>
          ))}
        </View>

        {error ? <Text style={{ color: theme.holiday, padding: 16 }}>Could not load the calendar ({error}).</Text> : null}
        {loading && !grid ? <ActivityIndicator style={{ padding: 24 }} color={theme.primary} /> : null}

        <View style={styles.grid}>
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
                <Text style={{ fontSize: 16, opacity: c.inMonth ? 1 : 0.4, color: selected ? theme.onPrimary : red ? theme.holiday : theme.text }}>
                  {d(c.day)}
                </Text>
                {secondary !== null && (
                  <Text style={{ fontSize: 9, color: selected ? theme.onPrimary : theme.muted }}>{d(secondary)}</Text>
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
  row: { flexDirection: "row" },
  weekday: { width: "14.2857%", textAlign: "center", fontSize: 12, paddingVertical: 4 },
  grid: { flexDirection: "row", flexWrap: "wrap" },
  cell: { width: "14.2857%", aspectRatio: 1, alignItems: "center", justifyContent: "center", borderRadius: 8 },
  dot: { width: 4, height: 4, borderRadius: 2, marginTop: 2 },
});
```

## 5. Using it in a screen

`App.tsx` (or any screen):

```tsx
import { useState } from "react";
import { FlatList, Pressable, SafeAreaView, Text, View } from "react-native";
import { calendarApi } from "./lib/api";
import { useCalendarTheme } from "./lib/use-theme";
import { useMonth } from "./lib/use-calendar";
import { DatePickerSheet, type DateValue } from "./components/DatePickerSheet";

export default function App() {
  const theme = useCalendarTheme(calendarApi);
  const [open, setOpen] = useState(false);
  const [date, setDate] = useState<DateValue | null>(null);

  // Events of the chosen BS month (or nothing until a date is picked).
  const [y, m] = date ? date.bs.split("-").map(Number) : [null, null];
  const { grid } = useMonth(calendarApi, "BS", y ?? null, m ?? null);

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: theme.tokens.bg, padding: 16 }}>
      <Pressable onPress={() => setOpen(true)}
        style={{ borderWidth: 1, borderColor: theme.tokens.border, borderRadius: theme.radius, padding: 12 }}>
        <Text style={{ color: theme.tokens.text }}>
          {date ? `BS ${date.bs}  ·  AD ${date.ad}` : "Choose a date"}
        </Text>
      </Pressable>

      <FlatList
        style={{ marginTop: 16 }}
        data={grid?.events ?? []}
        keyExtractor={(e) => e.id}
        renderItem={({ item }) => (
          <View style={{ borderLeftWidth: 4, borderLeftColor: theme.scheme === "dark" ? item.color.dark : item.color.light, paddingLeft: 8, marginBottom: 8 }}>
            <Text style={{ color: item.isHoliday ? theme.tokens.holiday : theme.tokens.text }}>
              {item.start.bs}  {item.title[theme.locale] ?? item.title.en}
            </Text>
          </View>
        )}
      />

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
      />
    </SafeAreaView>
  );
}
```

Run it with `npx expo start` and open it in Expo Go, an emulator or a simulator.

## 6. Refresh and offline behaviour

**Refresh when the app comes back.** Admin changes (new holidays, a new theme) should appear when the
user returns to the app. Clear the client's cache when the app becomes active, and re-mount the calendar
screen so it reloads:

```tsx
import { useEffect, useState } from "react";
import { AppState } from "react-native";
import { calendarApi } from "./lib/api";

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
  return refreshKey; // use as a React key: <CalendarScreen key={refreshKey} />
}
```

**Show the last months even without internet.** Save each month grid with AsyncStorage and fall back to
it when the request fails. Use this in `use-calendar.ts` instead of `api.month`:

```ts
import AsyncStorage from "@react-native-async-storage/async-storage";
import type { Basis, CalendarApi, MonthGrid } from "./calendar-api";

export async function monthWithOfflineCache(api: CalendarApi, basis: Basis, year: number, month: number): Promise<MonthGrid> {
  const key = `calendar-month:${basis}:${year}-${month}`;
  try {
    const grid = await api.month(basis, year, month);
    AsyncStorage.setItem(key, JSON.stringify(grid)).catch(() => {});
    return grid;
  } catch (err) {
    const saved = await AsyncStorage.getItem(key);
    if (saved) return JSON.parse(saved) as MonthGrid;
    throw err;
  }
}
```

**Fully offline apps** (the picker must work with no network at all, for any month) can download the
whole year table once (`GET /v1/calendar/data/latest`, about 10 KB), store it, and build month grids on
the phone with the conversion described in [how-it-works.md §4](how-it-works.md#4-how-a-date-is-converted).
`GET /v1/manifest` tells the app when the table changes (`dataVersion`). Most apps do not need this.

## 7. Checklist and common problems

| Symptom | Cause and fix |
|---------|---------------|
| `Network request failed` on Android emulator | Use `http://10.0.2.2:8080`, not `localhost`. |
| Works in development, fails in the store build | Release builds require `https`. Point `EXPO_PUBLIC_CAL_API` at the production HTTPS URL. |
| `401 INVALID_API_KEY` | Wrong key, or `.env` changed without restarting Metro (`npx expo start -c`). |
| `403 ORIGIN_NOT_ALLOWED` | You used the website's key. Mobile apps need a key without `allowedOrigins`. |
| Theme ignores dark mode | Set `"userInterfaceStyle": "automatic"` in `app.json`, otherwise the phone always reports light. |
| Nepali digits show as boxes | The device font lacks Devanagari. Bundle a font such as Noto Sans Devanagari with `expo-font`. |
